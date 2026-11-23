package webfront

import "testing"

func TestAnswerToolReachesOut(t *testing.T) {
	for _, html := range []string{
		`<input id="d" type="number"><output id="o"></output><script>d.oninput = () => { o.textContent = Number(d.value) * 2; };</script>`,
		`<label>Pick a location <select><option>Seattle</option></select></label>`,
	} {
		if answerToolReachesOut(html) {
			t.Errorf("a self-contained tool was refused: %s", html)
		}
	}
	for _, html := range []string{
		`<script>fetch('/api/x')</script>`,
		`<script>const pc = new RTCPeerConnection({iceServers:[{urls:'stun:stun.example.com'}]});</script>`,
		`<script>new webkitRTCPeerConnection({})</script>`,
		`<button onclick="RTCDataChannel">Go</button>`,
	} {
		if !answerToolReachesOut(html) {
			t.Errorf("a tool that reaches out was accepted: %s", html)
		}
	}
}
