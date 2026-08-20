module github.com/bluefunda/btp-go/sshclient

go 1.25.10

require (
	github.com/bluefunda/btp-go/connectivity v0.1.3
	github.com/bluefunda/btp-go/destination v0.3.0
	golang.org/x/crypto v0.55.0
)

require golang.org/x/sys v0.47.0 // indirect

replace (
	github.com/bluefunda/btp-go/connectivity => ../connectivity
	github.com/bluefunda/btp-go/destination => ../destination
)
