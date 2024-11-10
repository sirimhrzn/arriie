package main

import (
	"bytes"
	// "encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
)

func main() {

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
		go HandleConn(conn)
	}
}

type IncomingBuffer struct {
	Offset uint16
	Buffer []byte
}

func HandleConn(client net.Conn) {
	//<shared>
	var endOfFirstLine bool
	var endOfHeaders bool
	data := make([]byte, 0)

	bufferChan := make(chan IncomingBuffer, 0)
	endOfIncomingBufferStream := make(chan struct{})

	clientHTTPInfo := &ClientRequestInfo{}
	//<shared/>

	//<upstream>
	upstream, err := net.Dial("tcp", "localhost:8080")
	if err != nil {
		log.Printf("Failed to connect to upstream: %s", err)
		return
	}
	//<upstream/>
	//
	// //<mirror>
	go func() {
		mirror, err := net.Dial("tcp", "localhost:8080")
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
						endOfRequestLine := buffer.Buffer[idx:]
						log.Printf("after r: %s",string(endOfRequestLine))
						clientHTTPInfo.Uri = "/request-from-mirror"
						requestLine := []byte(clientHTTPInfo.IntoRequest())
						requestLine = append(requestLine,endOfRequestLine...)
						uriModified = true
						buffer.Buffer = requestLine
						break
					}
				}
			}
			log.Println(string(buffer.Buffer))
			mirror.Write(buffer.Buffer)
		case <-endOfIncomingBufferStream:
			log.Println("Finished reading client incoming buffer")
			break
		default:
		}
	}()
	//<mirror/>

	contentLength := 0
	headerEndIdx := 0
outer:
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
		upstream.Write(buffer)
		if contentLength != 0 && len(data[headerEndIdx:]) == contentLength {
			for {
				upstreamBuffer := make([]byte, 1024)
				upstreamOffset, err := upstream.Read(upstreamBuffer)
				if err != nil {
					if err.Error() == "EOF" {
						log.Println("Connection closed")
						break
					}
					log.Printf("Error reading: %s", err)
					break outer
				}
				if upstreamOffset > 0 {
					log.Printf("read %d bytes",upstreamOffset)
					// break
					_, err := client.Write(upstreamBuffer[:upstreamOffset])
					if err != nil {
						break outer
					}
				}
			}
			// break
		}
	}
	// the above loop breaks if all the data is written to upstream
	// and now its time to start reading from upstream
	// log.Println(string(data))
	// clientJSONHttpInfo, _ := json.MarshalIndent(&clientHTTPInfo, " ", " ")
	// log.Println(string(clientJSONHttpInfo))

}

type ClientRequestInfo struct {
	Method string
	Uri    string
}

func (r ClientRequestInfo) IntoRequest() string {
	return fmt.Sprintf("%s %s HTTP/1.1", r.Method, r.Uri)

}
