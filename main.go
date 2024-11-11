package main

import (
	"bytes"
	"os"
	"strings"
	"time"

	// "encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"

	"gopkg.in/yaml.v3"
)

func main() {


	appConfig := GetAppConfig()
	log.Println("config loaded")
	port := 8888
	addr := net.TCPAddr{
		Port: port,
	}
	log.Printf("Listening on port %d", port)

	listener, err := net.ListenTCP("tcp", &addr)
	if err != nil {
		log.Fatalln(err)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept Error: %s \n", err)
		}
		log.Printf("Accepting connection: %s", conn.RemoteAddr())
		go HandleConn(conn,appConfig)
	}
}

type IncomingBuffer struct {
	Offset uint16
	Buffer []byte
}

func HandleConn(client net.Conn,app *AppConfig) {
	now := time.Now()

	defer func() {
		log.Printf("Finished processing upstream request: %dms",time.Now().Sub(now).Milliseconds())
		client.Close()
	}()
	//<shared>
	var endOfFirstLine bool
	data := make([]byte, 0)

	bufferChan := make(chan IncomingBuffer, 0)
	endOfIncomingBufferStream := make(chan struct{})

	clientHTTPInfo := &ClientRequestInfo{}
	//<shared/>

	//<upstream>
	upstream, err := net.Dial("tcp", app.MirrorConfig.UpstreamAddr)
	if err != nil {
		log.Printf("Failed to connect to upstream: %s", err)
		return
	}
	//<upstream/>
	//
	// //<mirror>
	go func() {
		now := time.Now()

		defer func() {
			log.Printf("Finished processing mirror request: %dms",time.Now().Sub(now).Milliseconds())
		}()
		mirror, err := net.Dial("tcp", app.MirrorConfig.MirrorAddr)
		if err != nil {
			log.Printf("Failed to connect to upstream: %s", err)
			return
		}

		uriModified := false
		// bufferedData := []byte{}
		select {
		case buffer := <-bufferChan:
			if clientHTTPInfo.Uri != "" && !uriModified {
				for idx, buf := range buffer.Buffer {
					// search for first \r and swap uri in the mirror
					// possible error if first buffer does not contain the complete first line
					if buf == '\r' {
						if _, ok := app.MirrorConfig.URIMapping[clientHTTPInfo.Uri]; !ok  {
							// true since we dont need to modify & dont want this loop to run again
							uriModified = true
							break
						}
						// swapping uri
						finalURI := app.MirrorConfig.URIMapping[clientHTTPInfo.Uri]
						log.Printf("Swapping URI for mirror prev:%s final: %s",clientHTTPInfo.Uri,finalURI)

						// adjust offset according to the uri length diff
						if uriLengthDiff := len([]byte(finalURI)) - len([]byte(clientHTTPInfo.Uri)); uriLengthDiff < 0 {
							buffer.Offset -= uint16(-uriLengthDiff) 
						}else {
							buffer.Offset += uint16(uriLengthDiff)
						}

						clientHTTPInfo.Uri = finalURI

						endOfRequestLine := buffer.Buffer[idx:]
						// log.Printf("after r: %s",string(endOfRequestLine))
						requestLine := []byte(clientHTTPInfo.IntoRequest())
						requestLine = append(requestLine,endOfRequestLine...)
						uriModified = true
						buffer.Buffer = requestLine
						break
					}
				}
			}
			log.Println(string(buffer.Buffer))
			mirror.Write(buffer.Buffer[:buffer.Offset])
		case <-endOfIncomingBufferStream:
			log.Println("Finished reading client incoming buffer")
			break
		default:
		}
		mirrorResponseBuffer := make([]byte,0)
		contentLength := 0
		endOfHeaders := false
		for {
			buffer := make([]byte, 1024)
			offset, err := client.Read(buffer)
			if err != nil {
				if err.Error() == "EOF" {
					log.Println("Connection closed")
					break
				}
				log.Printf("Error reading: %s", err)
				break
			}
			mirrorResponseBuffer = append(mirrorResponseBuffer,buffer[:offset]...)
			headerEndIdx := bytes.Index(data, []byte("\r\n\r\n"))
			if headerEndIdx != -1 {
				endOfHeaders = true
			}
			// pushing index to actual end of header
			headerEndIdx += 4

			// if finished reading headers then search for contentLength value
			if endOfHeaders && contentLength == 0 {
				contentLengthHeader := "Content-Length: "
				contentLengthHeaderKeyLength := len(contentLengthHeader)
				// content length index
				clIdx := bytes.Index(data[:headerEndIdx], []byte(contentLengthHeader))
				if clIdx == -1 {
					//write error message here for no content length header so invalid http request
					log.Fatalln("invalid response, header 'Content-Length' not found")
				}
				begginningIdx := clIdx + contentLengthHeaderKeyLength
				for idx, b := range data[begginningIdx:headerEndIdx] {
					if b == '\r' {
						// log.Printf("reached end of content length, value: %s", string(data[begginningIdx:begginningIdx+idx]))
						length, err := strconv.Atoi(string(data[begginningIdx : begginningIdx+idx]))
						if err != nil {
							//write error message here for no content length header so invalid http request
							log.Fatalln("invalid request, failed to parse content length value to int")
						}
						contentLength = length
						// log.Println("finished recieving response from mirror")
						break
					}
				}
			}

		}

	}()
	//<mirror/>

	contentLength := 0
	var endOfHeaders bool
	headerEndIdx := 0
outer:
	for {
		buffer := make([]byte, 1024)
		offset, err := client.Read(buffer)
		if err != nil {
			if err.Error() == "EOF" {
				log.Println("Connection closed")
				return
			}
			log.Printf("Error reading: %s", err)
			break
		}
		if offset <= 0 {
			continue
		}

		data = append(data, buffer[:offset]...)

		if !endOfFirstLine {
			for idx, b := range data {
				if b == '\r' &&
					len(data[idx:]) >= 2 &&
					data[idx+1] == '\n' {
					endOfFirstLine = true
				}
			}
		}

		// Extract URI & Request Method from first line
		// yes i know i could used strings.Split
		if endOfFirstLine && clientHTTPInfo.Method == "" {
			prevWhiteSpaceIdx := 0
			for i := 0; i <= len(data)-1; i++ {
				if data[i] == ' ' && clientHTTPInfo.Method == "" {
					clientHTTPInfo.Method = string(data[:i])
					prevWhiteSpaceIdx = i
					continue
				}

				if data[i] == ' ' &&
					clientHTTPInfo.Method != "" &&
					clientHTTPInfo.Uri == "" {
					clientHTTPInfo.Uri = string(data[prevWhiteSpaceIdx+1 : i])
					break
				}
			}

		}

		if !endOfHeaders && clientHTTPInfo.Method != "GET" {
			headerEndIdx = bytes.Index(data, []byte("\r\n\r\n"))
			if headerEndIdx != -1 {
				endOfHeaders = true
			}
			// pushing index to actual end of header
			headerEndIdx += 4

			// if finished reading headers then search for contentLength value
			if endOfHeaders && contentLength == 0 {
				contentLengthHeader := "Content-Length: "
				contentLengthHeaderKeyLength := len(contentLengthHeader)
				// content length index
				clIdx := bytes.Index(data[:headerEndIdx], []byte(contentLengthHeader))
				if clIdx == -1 {
					//write error message here for no content length header so invalid http request
					log.Fatalln("invalid request, header 'Content-Length' not found")
				}
				begginningIdx := clIdx + contentLengthHeaderKeyLength
				for idx, b := range data[begginningIdx:headerEndIdx] {
					if b == '\r' {
						log.Printf("reached end of content length, value: %s", string(data[begginningIdx:begginningIdx+idx]))
						length, err := strconv.Atoi(string(data[begginningIdx : begginningIdx+idx]))
						if err != nil {
							//write error message here for no content length header so invalid http request
							log.Fatalln("invalid request, failed to parse content length value to int")
						}
						contentLength = length
						break
					}
				}
			}
		}
		// send this buffer to this channels the mirror can send request too
		bufferChan <- IncomingBuffer {
			Buffer: buffer,
			Offset: uint16(offset),
		}
		upstream.Write(buffer[:offset])
		log.Printf("Writing \n%s",string(buffer[:offset]))

		// checking if content length vaue matches the length after \r\n\r\n
		if contentLength != 0 && len(data[headerEndIdx:]) == contentLength {
			for {
				upstreamBuffer := make([]byte, 1024)
				upstreamOffset, err := upstream.Read(upstreamBuffer)
				if err != nil {
					if err.Error() == "EOF" {
						upstream.Close()
						log.Println("Connection closed with upstream")
						break outer
					}
					log.Printf("Error reading: %s", err)
					break
				}
				if upstreamOffset > 0 {
					log.Printf("read %d bytes",upstreamOffset)
					// break
					_, err := client.Write(upstreamBuffer[:upstreamOffset])
					// if err != nil {
					// 	break outer
					// }
					if err != nil {
						log.Println("Client connection  closed")
						break outer
					}
					log.Printf("Error reading: %s", err)
					break
				}
			}
			break
		}
	}
	log.Println("END OF HANDLE CONNNN")
	return 
	// the above loop breaks if all the data is written to upstream
	// and now its time to start reading from upstream
	log.Println(string(data))
	// clientJSONHttpInfo, _ := json.MarshalIndent(&clientHTTPInfo, " ", " ")
	// log.Println(string(clientJSONHttpInfo))

}


func GetAppConfig() *AppConfig {
	configPath := ""
	for _, arg := range os.Args {
		if strings.Contains(arg,"--config=") {
			configPath = strings.Split(arg,"=")[1]
		}
	}
	if configPath == "" {
		log.Fatalln("Provide config file path. Example: --config=<file-path>")
	}
	fileBuffer, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalln(err)
	}
	appConfig := &AppConfig{}
	if err := yaml.Unmarshal(fileBuffer,&appConfig); err != nil {
		log.Fatalln(err)
	}
	return appConfig
}
type ClientRequestInfo struct {
	Method string
	Uri    string
}

func (r ClientRequestInfo) IntoRequest() string {
	return fmt.Sprintf("%s %s HTTP/1.1", r.Method, r.Uri)
}

 
type AppConfig struct {
	MirrorConfig MirrorConfig `yaml:"mirror_config"`
}

type MirrorConfig struct {
	UpstreamAddr string            `yaml:"upstream_addr"`
	MirrorAddr   string            `yaml:"mirror_addr"`
	URIMapping   map[string]string `yaml:"uri_mapping"`
}

// func LoadEnv() {
// 	args := os.Args
// 	envLoaded := false
// 	test := false
// 	for _, arg := range args {
// 		if strings.Contains(arg, "--env=") {
// 			envFile := strings.Split(arg, "=")[1]
// 			err := godotenv.Load(envFile)
// 			if err != nil {
// 				log.Fatalln(err)
// 			}
// 			envLoaded = true
// 		}
// 		if arg == "--test" {
// 			test = true
// 		}
// 	}
// 	if test {
// 		cwd, err := os.Getwd()
// 		if err != nil {
// 			log.Fatal(err)
// 		}
// 		env := fmt.Sprintf("%s/.env",cwd)
// 		godotenv.Load(env)
// 	}
// 	if !envLoaded && !test {
// 		log.Fatalln("Specify env file: --env=<file_path>")
// 	}
// }


