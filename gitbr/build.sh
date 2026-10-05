#!/bin/bash

GO111MODULE=on go mod tidy -compat=1.17 && GO111MODULE=on go build .
