package geecache

type PeerGetter interface {
	Get(group string, key string) ([]byte, error)
}

type PeerPicker interface {
	PickPeer(key string) (peer PeerGetter, ok bool)
}

type PeerRemover interface {
	RemovePeer(peer string)
}
