//go:build windows && amd64

package lan

func (s *controllerSession) setTransferNotice(text string) {
	s.noticeMu.Lock()
	s.transferNotice = text
	s.noticeMu.Unlock()
}
func (s *controllerSession) setClipboardNotice(text string) {
	s.noticeMu.Lock()
	s.clipboardNotice = text
	s.noticeMu.Unlock()
}
func (s *controllerSession) notices() string {
	s.noticeMu.Lock()
	defer s.noticeMu.Unlock()
	text := s.transferNotice
	if s.clipboardNotice != "" {
		if text != "" {
			text += " | "
		}
		text += s.clipboardNotice
	}
	return text
}
func (s *controllerSession) networkTotals() (rx, tx uint64) {
	rx, tx = s.udpRx.Load(), s.udpTx.Load()
	for _, w := range []*wire{s.ctrl, s.video} {
		if w != nil {
			if m := meter(w.conn); m != nil {
				rx += m.rx.Load()
				tx += m.tx.Load()
			}
		}
	}
	s.auxMu.Lock()
	defer s.auxMu.Unlock()
	rx += s.auxRx
	tx += s.auxTx
	for m := range s.auxMeters {
		rx += m.rx.Load()
		tx += m.tx.Load()
	}
	return
}
