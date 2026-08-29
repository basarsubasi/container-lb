package main
import (
	"fmt"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)
func main() {
	ns, _ := netns.GetFromName("test_ns")
	defer ns.Close()
	h, _ := netlink.NewHandleAt(ns)
	defer h.Delete()
	links, _ := h.LinkList()
	for _, l := range links {
		fmt.Printf("link %s, index: %d, parent: %d, type: %s\n", l.Attrs().Name, l.Attrs().Index, l.Attrs().ParentIndex, l.Type())
	}
}
