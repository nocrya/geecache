package geecache

type PeerGetter interface {
	Peer() string
	Proto() string
	Get(group string, key string) ([]byte, error)
}

type PeerPicker interface {
	PickPeer(key string) (peer PeerGetter, addr string)
}

type PeerRemover interface {
	RemovePeer(peer string)
}

type PeerWriter interface {
	IsOwner(key string) bool
	ForwardSet(group, key string, value []byte) error
	ForwardPurge(group, key string) error
	BroadcastInvalidate(group, key string) error
}
