# Test server

This is a very simple mock-up of a server that supports the following
HTTP requests.

- `GET /health`: responds with a 503 if invoked too early in the
  server's lifetime, a 200 otherwise. The startup delay is specified
  on the command line.

- `GET /is_sleeping`: responds with a 200 status code and the JSON
  representation of the current `sleepState` defined in [the
  code](main.go).

- `POST /sleep`: sets the "is-sleeping" bit to true.

- `POST /wake_up`: sets the "is-sleeping" bit to false, which is the
  initial state.

## Requester specific arguments

```console
      --port int16                       port at which to listen for HTTP connections (default 8000)
      --startup-delay int                number of seconds to delay before positive responses to /health (default 47)
```

## Logging arguments

```console
      --add_dir_header                   If true, adds the file directory to the header of the log messages
      --alsologtostderr                  log to standard error as well as files (no effect when -logtostderr=true)
      --log_backtrace_at traceLocation   when logging hits line file:N, emit a stack trace (default :0)
      --log_dir string                   If non-empty, write log files in this directory (no effect when -logtostderr=true)
      --log_file string                  If non-empty, use this log file (no effect when -logtostderr=true)
      --log_file_max_size uint           Defines the maximum size a log file can grow to (no effect when -logtostderr=true). Unit is megabytes. If the value is 0, the maximum file size is unlimited. (default 1800)
      --logtostderr                      log to standard error instead of files (default true)
      --one_output                       If true, only write logs to their native severity level (vs also writing to each lower severity level; no effect when -logtostderr=true)
      --skip_headers                     If true, avoid header prefixes in the log messages
      --skip_log_headers                 If true, avoid headers when opening log files (no effect when -logtostderr=true)
      --stderrthreshold severity         logs at or above this threshold go to stderr when writing to files and stderr (no effect when -logtostderr=true or -alsologtostderr=true) (default 2)
  -v, --v Level                          number for the log level verbosity
      --vmodule moduleSpec               comma-separated list of pattern=N settings for file-filtered logging
```
