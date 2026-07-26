package daemon

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/peterkure3/wmux/internal/proto"
)

func newTestSurface(cols, rows int) *Surface {
	return &Surface{
		emu:         vt.NewEmulator(cols, rows),
		cols:        cols,
		rows:        rows,
		clients:     make(map[chan proto.SurfaceFrame]struct{}),
		cellClients: make(map[chan proto.CellsFrame]struct{}),
	}
}

// reconstructRow rebuilds a row's plain text purely from Runs, the way a
// rich client would draw it — the thing this whole feature exists to make
// cheap and correct.
func reconstructRow(runs []proto.Run, cols int) string {
	buf := make([]rune, cols)
	for i := range buf {
		buf[i] = ' '
	}
	for _, run := range runs {
		x := run.X
		for _, r := range run.Text {
			if x >= cols {
				break
			}
			buf[x] = r
			x++
		}
	}
	return string(buf)
}

// rowTextFromEmulator reads a row directly off the emulator via CellAt —
// the authoritative source of truth Runs must reproduce exactly.
func rowTextFromEmulator(sfc *Surface, y, cols int) string {
	buf := make([]rune, cols)
	for i := range buf {
		buf[i] = ' '
	}
	for x := 0; x < cols; x++ {
		cell := sfc.emu.CellAt(x, y)
		if cell == nil || cell.Content == "" {
			continue
		}
		buf[x] = []rune(cell.Content)[0]
	}
	return string(buf)
}

// TestCellsReplayReconstructsEmulatorGrid is the contract test the phased
// refactor plan calls for: drive the emulator with known content
// (including styled text), build a cells replay, and assert the grid
// reconstructed purely from Runs matches what emu.CellAt() actually
// holds — cell for cell, not just "looks similar".
func TestCellsReplayReconstructsEmulatorGrid(t *testing.T) {
	sfc := newTestSurface(20, 5)
	sfc.emu.WriteString("plain\r\n\x1b[31mred\x1b[0m ok\r\n\x1b[1mbold\x1b[0m")

	sfc.mu.Lock()
	frame := sfc.cellsReplayFrameLocked()
	sfc.mu.Unlock()

	if frame.Type != proto.CellsReplay {
		t.Fatalf("Type = %q, want %q", frame.Type, proto.CellsReplay)
	}
	if frame.Cols != 20 || frame.RowCnt != 5 {
		t.Fatalf("dimensions = %dx%d, want 20x5", frame.Cols, frame.RowCnt)
	}
	if len(frame.Rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(frame.Rows))
	}

	for y := 0; y < 5; y++ {
		got := reconstructRow(frame.Rows[y].Runs, 20)
		want := rowTextFromEmulator(sfc, y, 20)
		if got != want {
			t.Errorf("row %d: reconstructed %q from Runs, want %q from emu.CellAt", y, got, want)
		}
	}

	// Styling must actually travel, not just text — otherwise this test
	// would still pass with styling silently dropped.
	var sawColor, sawBold, sawPlain bool
	for _, run := range frame.Rows[0].Runs {
		if strings.Contains(run.Text, "plain") && run.FG == "" {
			sawPlain = true
		}
	}
	for _, run := range frame.Rows[1].Runs {
		if strings.Contains(run.Text, "red") && run.FG != "" {
			sawColor = true
		}
	}
	for _, run := range frame.Rows[2].Runs {
		if strings.Contains(run.Text, "bold") && run.Attrs&proto.AttrBold != 0 {
			sawBold = true
		}
	}
	if !sawPlain {
		t.Error("plain text run unexpectedly carries a foreground color")
	}
	if !sawColor {
		t.Error("red text run does not carry a foreground color")
	}
	if !sawBold {
		t.Error("bold text run does not carry AttrBold")
	}
}

// TestCellsUpdateOnlyNamesChangedRows is the damage-tracking contract:
// rewriting one row must produce an update frame naming exactly that row,
// not every row on screen.
func TestCellsUpdateOnlyNamesChangedRows(t *testing.T) {
	sfc := newTestSurface(10, 3)
	sfc.emu.WriteString("row0\r\nrow1\r\nrow2")

	sfc.mu.Lock()
	sfc.cellsReplayFrameLocked() // establishes the baseline cellSnapshot
	sfc.mu.Unlock()

	ch := make(chan proto.CellsFrame, 4)
	sfc.mu.Lock()
	sfc.cellClients[ch] = struct{}{}
	sfc.mu.Unlock()

	// Rewrite only the middle row (cursor to row 2, col 1 — 1-indexed).
	sfc.mu.Lock()
	sfc.emu.WriteString("\x1b[2;1Hchanged!!!")
	sfc.broadcastCellsUpdateLocked()
	sfc.mu.Unlock()

	select {
	case frame := <-ch:
		if frame.Type != proto.CellsUpdate {
			t.Fatalf("Type = %q, want %q", frame.Type, proto.CellsUpdate)
		}
		if len(frame.Rows) != 1 || frame.Rows[0].Y != 1 {
			t.Fatalf("changed rows = %+v, want exactly row 1", frame.Rows)
		}
	default:
		t.Fatal("broadcastCellsUpdateLocked sent nothing after a real change")
	}
}

// TestBroadcastCellsUpdateSkipsWorkWithNoClients guards the cost claim in
// broadcastCellsUpdateLocked's doc comment: a plain `wmux connect`
// session (no cells client attached) must not pay for the row scan.
func TestBroadcastCellsUpdateSkipsWorkWithNoClients(t *testing.T) {
	sfc := newTestSurface(10, 3)
	sfc.emu.WriteString("hello")

	sfc.mu.Lock()
	sfc.broadcastCellsUpdateLocked()
	sfc.mu.Unlock()

	if sfc.cellSnapshot != nil {
		t.Fatal("cellSnapshot was populated despite no cells client being attached")
	}
}

// TestSpawnSurfaceCellsAttachEndToEnd exercises the full path a real
// client uses: SpawnSurface, AttachSurfaceCells, read the replay, then
// read frames until the process's own output ("hi", from `echo hi`)
// shows up in a cells frame and the surface exits. Cross-platform: on
// Windows this runs natively via cmd.exe (no WSL dependency); elsewhere
// buildSurfaceCommand always goes through bash regardless of Native.
func TestSpawnSurfaceCellsAttachEndToEnd(t *testing.T) {
	d := New("", "")
	req := proto.NewSurfaceRequest{
		ID: "cellstest", Cwd: t.TempDir(), Command: "echo hi",
		Native: runtime.GOOS == "windows", Cols: 40, Rows: 10,
	}
	if _, err := d.SpawnSurface(req); err != nil {
		t.Fatalf("SpawnSurface: %v", err)
	}
	t.Cleanup(func() { d.Close("cellstest") })

	ch, err := d.AttachSurfaceCells("cellstest")
	if err != nil {
		t.Fatalf("AttachSurfaceCells: %v", err)
	}
	defer d.DetachSurfaceCells("cellstest", ch)

	first := <-ch
	if first.Type != proto.CellsReplay {
		t.Fatalf("first frame type = %q, want %q", first.Type, proto.CellsReplay)
	}
	if len(first.Rows) != 10 {
		t.Fatalf("replay rows = %d, want 10", len(first.Rows))
	}

	deadline := time.After(5 * time.Second)
	sawHi := false
loop:
	for {
		select {
		case frame := <-ch:
			for _, row := range frame.Rows {
				for _, run := range row.Runs {
					if strings.Contains(run.Text, "hi") {
						sawHi = true
					}
				}
			}
			if frame.Type == proto.CellsExit {
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for the surface to exit")
		}
	}
	if !sawHi {
		t.Fatal("process exited without \"hi\" ever appearing in a cells frame")
	}
}
