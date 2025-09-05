package api

// This file defines interface of the stub.

// AcceleratorQueryPath is the path part of the URL that is polled
// to get the server-requesting container's associated accelerators.
// The scheme of the URL is HTTP.
// The host part of the URL is something that refers to the
// server-requesting container.
// A 200 response's body must contain a JSON array of strings.
// Each string identifies one accelerator, in a way that
// is appropriate for the software used to access the accelerators.
const AcceleratorQueryPath = "/dual-pod/accelerators"
