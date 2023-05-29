package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/util"
	quicTransport "github.com/libp2p/go-libp2p/p2p/transport/quic"
	tcpTransport "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	webTransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	"github.com/multiformats/go-multiaddr"
)

// DiscoveryInterval is how often we search for other peers via the DHT.
const DiscoveryInterval = time.Second * 10

// DiscoveryServiceTag is used in our mDNS / DHT advertisements to discover
// other peers.
const DiscoveryServiceTag = "universal-connectivity"

// discoveryNotifee gets notified when we find a new peer via mDNS discovery.
type discoveryNotifee struct {
	h host.Host
}

// Borrowed from https://medium.com/rahasak/libp2p-pubsub-peer-discovery-with-kademlia-dht-c8b131550ac7
// NewDHT attempts to connect to a bunch of bootstrap peers and returns a new DHT.
// If you don't have any bootstrapPeers, you can use dht.DefaultBootstrapPeers
// or an empty list.
func NewDHT(ctx context.Context, host host.Host, bootstrapPeers []multiaddr.Multiaddr) (*dht.IpfsDHT, error) {
	var options []dht.Option

	// if no bootstrap peers, make this peer act as a bootstraping node
	// other peers can use this peers ipfs address for peer discovery via dht
	if len(bootstrapPeers) == 0 {
		options = append(options, dht.Mode(dht.ModeServer))
	}

	// set our DiscoveryServiceTag as the protocol prefix so we can discover
	// peers we're interested in.
	options = append(options, dht.ProtocolPrefix("/"+DiscoveryServiceTag))

	kdht, err := dht.New(ctx, host, options...)
	if err != nil {
		return nil, err
	}

	if err = kdht.Bootstrap(ctx); err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	// loop through bootstrapPeers (if any), and attempt to connect to them
	for _, peerAddr := range bootstrapPeers {
		peerinfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := host.Connect(ctx, *peerinfo); err != nil {
				fmt.Printf("Error while connecting to node %q: %-v", peerinfo, err)
				fmt.Println()
			} else {
				fmt.Printf("Connection established with bootstrap node: %q", *peerinfo)
				fmt.Println()
			}
		}()
	}
	wg.Wait()

	return kdht, nil
}

// Borrowed from https://medium.com/rahasak/libp2p-pubsub-peer-discovery-with-kademlia-dht-c8b131550ac7
// Search the DHT for peers, then connect to them.
func Discover(ctx context.Context, h host.Host, dht *dht.IpfsDHT, rendezvous string) {
	var routingDiscovery = routing.NewRoutingDiscovery(dht)

	// Advertise our addresses on rendezvous
	discovery.Advertise(ctx, routingDiscovery, rendezvous)

	// Search for peers every DiscoveryInterval
	ticker := time.NewTicker(DiscoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:

			// Search for other peers advertising on rendezvous and
			// connect to them.
			peers, err := discovery.FindPeers(ctx, routingDiscovery, rendezvous)
			if err != nil {
				panic(err)
			}

			for _, p := range peers {
				if p.ID == h.ID() {
					continue
				}
				if h.Network().Connectedness(p.ID) != network.Connected {
					_, err = h.Network().DialPeer(ctx, p.ID)
					if err != nil {
						fmt.Printf("Failed to connect to peer (%s): %s", p.ID, err.Error())
						fmt.Println()
						continue
					}
					fmt.Println("Connected to peer", p.ID.Pretty())
				}
			}
		}
	}
}

// HandlePeerFound connects to peers discovered via mDNS. Once they're connected,
// the PubSub system will automatically start interacting with them if they also
// support PubSub.
func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	fmt.Println("discovered new peer", pi.ID.Pretty())
	err := n.h.Connect(context.Background(), pi)
	if err != nil {
		fmt.Printf("error connecting to peer %s: %s", pi.ID.Pretty(), err)
		fmt.Println()
	}
}

// setupDiscovery creates an mDNS discovery service and attaches it to the libp2p
// Host. This lets us automatically discover peers on the same LAN and connect to
// them.
func setupDiscovery(h host.Host) error {
	// setup mDNS discovery to find local peers
	s := mdns.NewMdnsService(h, DiscoveryServiceTag, &discoveryNotifee{h: h})
	return s.Start()
}

func main() {
	_ = context.Background()

	// Load our private key from "identity.key", if it doesn't exist,
	// generate one, and store it in "identity.key".
	privk, err := LoadIdentity("identity.key")
	if err != nil {
		panic(err)
	}

	var opts []libp2p.Option

	announceAddrs := []string{} // Set to your external IP address for each transport you wish to use.
	var announce []multiaddr.Multiaddr
	if len(announceAddrs) > 0 {
		for _, addr := range announceAddrs {
			announce = append(announce, multiaddr.StringCast(addr))
		}
		opts = append(opts, libp2p.AddrsFactory(func([]multiaddr.Multiaddr) []multiaddr.Multiaddr {
			return announce
		}))
	}

	opts = append(opts,
		libp2p.Identity(privk),
		libp2p.Transport(tcpTransport.NewTCPTransport),
		libp2p.Transport(quicTransport.NewTransport),
		libp2p.Transport(webTransport.New),
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/9090", "/ip4/0.0.0.0/udp/9091/quic-v1", "/ip4/0.0.0.0/udp/9092/quic-v1/webtransport"),
	)

	// Create a new libp2p Host with our options.
	h, err := libp2p.New(opts...)
	if err != nil {
		panic(err)
	}

	// Create a new PubSub service using the GossipSub router.
	ps, err := pubsub.NewGossipSub(context.TODO(), h)
	if err != nil {
		panic(err)
	}

	// Join a PubSub topic.
	topicString := "UniversalPeer" // Change "UniversalPeer" to whatever you want!
	topic, err := ps.Join(DiscoveryServiceTag + "/" + topicString)
	if err != nil {
		panic(err)
	}

	// Publish the current date and time every 5 seconds.
	go func() {
		for {
			err := topic.Publish(context.TODO(), []byte(fmt.Sprintf("The time is: %s", time.Now().Format(time.RFC3339))))
			if err != nil {
				panic(err)
			}
			time.Sleep(time.Second * 5)
		}
	}()

	// Setup DHT with empty discovery peers so this will be a discovery peer for other
	// peers. This peer should run with a public ip address, otherwise change "nil" to
	// a list of peers to bootstrap with.
	dht, err := NewDHT(context.TODO(), h, nil)
	if err != nil {
		panic(err)
	}

	// Setup global peer discovery over DiscoveryServiceTag.
	go Discover(context.TODO(), h, dht, DiscoveryServiceTag)

	// Setup local mDNS discovery.
	if err := setupDiscovery(h); err != nil {
		panic(err)
	}

	fmt.Println("PeerID:", h.ID().String())
	for _, addr := range h.Addrs() {
		fmt.Printf("Listening on: %s/p2p/%s", addr.String(), h.ID())
		fmt.Println()
	}

	// Subscribe to the topic.
	sub, err := topic.Subscribe()
	if err != nil {
		panic(err)
	}

	for {
		// Block until we recieve a new message.
		msg, err := sub.Next(context.TODO())
		if err != nil {
			panic(err)
		}
		fmt.Printf("[%s] %s", msg.ReceivedFrom, string(msg.Data))
		fmt.Println()
	}
}
