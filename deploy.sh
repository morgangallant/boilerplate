#!/bin/bash

rm -rf server
GOOS=linux go build -o server .
if [ ! -f server ]; then
    echo "Compilation failed, terminating."
    exit 1
fi
for machine in 100.114.9.97 100.66.154.69 100.114.185.52; do
    scp server root@$machine:/root/server
done

