// Package network is the Sandbox Network Gateway: a user-mode TCP/IP stack
// (gVisor netstack) that is the Android guest's only network interface. It
// provides DHCP and DNS to the guest, terminates every guest TCP/UDP flow and
// re-originates it upstream while capturing HTTP(S)/WebSocket/DNS activity as
// normalized model.Events. Host (Windows) traffic never enters this stack.
package network
