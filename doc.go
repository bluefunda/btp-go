// Package btp is the landing page for the btp-go monorepo — a collection of
// small, stdlib-only Go libraries for consuming SAP BTP (Business Technology
// Platform) services from Cloud Foundry, Kyma, or any Go-capable runtime.
//
// # Modules
//
// Each library is an independent Go module so you only pull what you need:
//
//   - [github.com/bluefunda/btp-go/xsuaa] — fetch and cache client-credentials
//     tokens from SAP XSUAA
//   - [github.com/bluefunda/btp-go/connectivity] — open TCP tunnels through SAP
//     Cloud Connector via the SOCKS5 proxy (SAP 0x80 auth)
//   - [github.com/bluefunda/btp-go/destination] — look up named destinations
//     from the SAP Destination Service REST API
//   - [github.com/bluefunda/btp-go/binding] — parse service bindings from
//     VCAP_SERVICES (Cloud Foundry), Kyma file mounts, or auto-detect
//   - [github.com/bluefunda/btp-go/sshclient] — high-level SSH/SFTP client over
//     the Cloud Connector tunnel (wraps connectivity + destination)
//
// # Getting started
//
// Install the module you need:
//
//	go get github.com/bluefunda/btp-go/xsuaa@latest
//
// See the [examples/sftp-count] directory for a complete Cloud Foundry app that
// counts files on a remote SFTP server via the Cloud Connector.
package btp
