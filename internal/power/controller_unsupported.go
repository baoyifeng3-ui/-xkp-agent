//go:build !linux

package power

func NewController() Controller {
	return unsupportedController{}
}
