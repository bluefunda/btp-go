module github.com/bluefunda/btp-go/sshclient

go 1.25.0

require (
	github.com/bluefunda/btp-go/connectivity v0.0.0
	github.com/bluefunda/btp-go/destination v0.0.0
	golang.org/x/crypto v0.50.0
)

require golang.org/x/sys v0.43.0 // indirect

replace (
	github.com/bluefunda/btp-go/connectivity => ../connectivity
	github.com/bluefunda/btp-go/destination => ../destination
)
