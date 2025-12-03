//go:build darwin

package nat

func (n *NAT) listTapInterfaces() ([]TapInterface, error) {
	panic("listTapInterfaces is not implemented on darwin")
}
