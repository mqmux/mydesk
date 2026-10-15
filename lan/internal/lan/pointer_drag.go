package lan

// Local UI state, independent of keyboard-capture mode. Track every button so
// moving outside the picture/HUD cannot terminate a held remote drag or lose UP.
type pointerDrag struct {
	buttons uint8
	x, y    int
}

func (d *pointerDrag) event(m uint32, x, y int, inside bool) (inputEvent, bool) {
	e := inputEvent{Kind: "move", X: x, Y: y}
	switch m {
	case 0x200:
		if !inside && d.buttons == 0 {
			return e, false
		}
	case 0x201, 0x203, 0x204, 0x206, 0x207, 0x209:
		e.Kind = "down"
		if !inside {
			return e, false
		}
	case 0x202, 0x205, 0x208:
		e.Kind = "up"
	default:
		return e, false
	}
	if m == 0x204 || m == 0x205 || m == 0x206 {
		e.Button = 1
	} else if m == 0x207 || m == 0x208 || m == 0x209 {
		e.Button = 2
	}
	mask := uint8(1 << e.Button)
	if e.Kind == "up" {
		if d.buttons&mask == 0 {
			return e, false
		}
		d.buttons &^= mask
	} else if e.Kind == "down" {
		d.buttons |= mask
	}
	d.x, d.y = x, y
	return e, true
}
