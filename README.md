# Arriie

Minimal service to mirror HTTP requests.

### Why this?
See, I had this new highly optimized backwards compatible api ready for production and I wanted to load test this thing, but it was a `POST` request and clients request payload were somewhat unique to one another, so wrote this to mirror request to another server on the new endpoint. As of now, this can be possibly seen as tailored to fit my very specific needs but hopefully we can fix that with further iteration.

***
> [!NOTE]
> This service is built for maximum performance by minimizing HTTP parsing overhead. Instead, it directly modifies required values on the fly.
> Yes I've heard about of nginx mirror module but was not sure about configuring nginx modules for every server I might have to touch so (i dont wanna break prod), however using in production, do run it behind a reverse proxy as it does not support HTTPS.

