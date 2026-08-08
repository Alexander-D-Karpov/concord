// Package netinfo determines the host/IP a server should advertise and prints the
// startup access banner.
//
// ComputeAdvertised resolves the address by precedence: explicit config, then the
// CONCORD_PUBLIC_HOST env var, then an outbound public-IP probe, then the LAN
// address, then loopback. It makes real outbound network calls at startup (an
// HTTPS public-IP lookup and a UDP dial to read the local IP), so it has network
// side effects. Shared by any binary that advertises an address.
package netinfo
