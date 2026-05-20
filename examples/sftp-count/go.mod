module github.com/bluefunda/btp-go/examples/sftp-count

go 1.25.10

require (
	github.com/bluefunda/btp-go/binding v0.2.0
	github.com/bluefunda/btp-go/connectivity v0.1.3
	github.com/bluefunda/btp-go/destination v0.3.0
	github.com/bluefunda/btp-go/xsuaa v0.1.3
	github.com/pkg/sftp v1.13.7
	golang.org/x/crypto v0.50.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
)

replace (
	github.com/bluefunda/btp-go/binding => ../../binding
	github.com/bluefunda/btp-go/connectivity => ../../connectivity
	github.com/bluefunda/btp-go/destination => ../../destination
	github.com/bluefunda/btp-go/xsuaa => ../../xsuaa
)
