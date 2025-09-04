package api

// This file defines details of the reverse proxy.

// AcceleratorQueryPath is the path part of the URL that is polled
// to get the server-requesting container's associated accelerators.
// The scheme of the URL is HTTP.
// The host part of the URL is something that refers to the
// server-requesting container.
// A 200 response's body must contain a JSON array of strings.
// Each string identifies one accelerator, in a way that
// is appropriate for the software used to access the accelerators.
const AcceleratorQueryPath = "/dual-pod/accelerators"

// BackendPostPath is the path part of the URL that is used
// to POST the endpoint that the reverse-proxy should proxy to.
// The scheme of the URL is HTTP.
// The host part of the URL is something that refers to the
// server-requesting container.
// The body of the POST request is the JSON rendering of the `Endpoint`
// to proxy to.
// The reverse proxy responds to an incoming connection request
// to its incoming port by opening a new connection to the backend
// endpoint and, once both connections are open, connecting the
// byte streams in both directions.
const BackendPostPath = "/dual-pod/backend"

// Endpoint describes a TCP endpoint.
type Endpoint struct {
    Host string
    Port int16
}
