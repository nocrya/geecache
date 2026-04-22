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
