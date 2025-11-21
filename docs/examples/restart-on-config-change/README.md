# Restart llmsnap on config change

Sometimes editing the configuration file can take a bit of trail and error to get a model configuration tuned just right. The `watch-and-restart.sh` script can be used to watch `config.yaml` for changes and restart `llmsnap` when it detects a change.

```bash
#!/bin/bash
#
# A simple watch and restart llmsnap when its configuration
# file changes. Useful for trying out configuration changes
# without manually restarting the server each time.
if [ -z "$1" ]; then
    echo "Usage: $0 <path to config.yaml>"
    exit 1
fi

while true; do
    # Start the process again
    ./llmsnap-linux-amd64 -config $1 -listen :1867 &
    PID=$!
    echo "Started llmsnap with PID $PID"

    # Wait for modifications in the specified directory or file
    inotifywait -e modify "$1"

    # Check if process exists before sending signal
    if kill -0 $PID 2>/dev/null; then
        echo "Sending SIGTERM to $PID"
        kill -SIGTERM $PID
        wait $PID
    else
        echo "Process $PID no longer exists"
    fi
    sleep 1
done
```

## Usage and output example

```bash
$ ./watch-and-restart.sh config.yaml
Started llmsnap with PID 495455
Setting up watches.
Watches established.
llmsnap listening on :1867
Sending SIGTERM to 495455
Shutting down llmsnap
Started llmsnap with PID 495486
Setting up watches.
Watches established.
llmsnap listening on :1867
```
