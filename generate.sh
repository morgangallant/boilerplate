#!/bin/bash

mkdir -p internal/pb
protoc --go_out=. --go-grpc_out=. cluster.proto