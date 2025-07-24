# build

```shell
cargo build --release  
export CGO_LDFLAGS="-L target/release -lm -lrt"
GO111MODULE=on CGO_ENABLED=1 go build -a -ldflags="-extldflags=-ldl" -o main ./bench/*.go && ./main 2> output
```

# run

```shell
curl "http://localhost:6060/debug/pprof/profile?seconds=30" --output cpu-db.pprof
go tool pprof -http=:8001 cpu-db.pprof
curl "http://localhost:9096/debug/pprof/profile?seconds=30" --output cpu-kv.svg
```
