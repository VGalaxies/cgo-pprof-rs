# build

```shell
cargo build --release  
export CGO_LDFLAGS="-L target/release -lm -lrt"
GO111MODULE=on CGO_ENABLED=1 go build -a -ldflags="-extldflags=-ldl" -o main ./bench/main.go && ./main 2> output
```

# run

```shell
curl "http://localhost:9096/debug/pprof/profile?seconds=30" --output cpu.svg
```
