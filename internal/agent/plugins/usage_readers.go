package plugins

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	usageGuardBytes       = 64
	usageMaxLineBytes     = 16 * 1024 * 1024 // 16 MiB
	usageSinceGracePeriod = 36 * time.Hour
)

// AttachProviderUsageReaders attaches native agent.UsageReader implementations
// onto bp for supported providers when CustomUsageReader is nil.
func AttachProviderUsageReaders(bp *agent.BasePlugin) {
	if bp == nil || bp.CustomUsageReader != nil {
		return
	}
	switch bp.PluginSpec.ID {
	case "claude":
		bp.CustomUsageReader = NewClaudeUsageReader()
	case "codex":
		bp.CustomUsageReader = NewCodexUsageReader()
	case "opencode":
		bp.CustomUsageReader = NewOpenCodeUsageReader()
	case "antigravity":
		bp.CustomUsageReader = NewAntigravityUsageReader()
	case "copilot":
		bp.CustomUsageReader = NewCopilotUsageReader()
	case "grok":
		bp.CustomUsageReader = NewGrokUsageReader()
	case "pi":
		bp.CustomUsageReader = NewPiUsageReader()
	}
}

type jsonlScanHooks struct {
	RestoreState func(stateJSON []byte) error
	SaveState    func() ([]byte, error)
	ParseLine    func(line []byte) (records []agent.UsageRecord, stop bool)
}

// scanJSONLFileIncremental scans a JSONL file with optional file-level caching
// and byte-offset incremental resume.
//
// Cache behavior:
//   - If size and mtime match the cached entry, cached.Records are returned
//     directly without opening or parsing the file.
//   - If the file grew and the 64-byte FNV-1a guard hash immediately before
//     cached.ResumeOffset still matches, parser state is restored from
//     cached.StateJSON and only newly appended complete lines are parsed.
//   - Otherwise, a full scan from offset 0 is performed.
//
// Lines larger than 16 MiB are safely skipped without failing the scan.
func scanJSONLFileIncremental(
	ctx context.Context,
	path string,
	cache agent.UsageFileCache,
	hooks jsonlScanHooks,
) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}

	size := info.Size()
	mtimeNs := info.ModTime().UnixNano()

	var (
		cached    agent.CachedFileUsage
		hasCached bool
	)
	if cache != nil {
		cached, hasCached = cache.Get(path)
		if hasCached && size == cached.Size && mtimeNs == cached.MtimeNs {
			out := make([]agent.UsageRecord, len(cached.Records))
			copy(out, cached.Records)
			return out, nil
		}
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var (
		startOffset int64
		records     []agent.UsageRecord
	)
	if hasCached &&
		size > cached.Size &&
		cached.ResumeOffset > 0 &&
		cached.ResumeOffset <= size &&
		cached.GuardLen > 0 &&
		cached.GuardLen <= usageGuardBytes {
		guardStart := cached.ResumeOffset - int64(cached.GuardLen)
		if guardStart >= 0 {
			guardBuf := make([]byte, cached.GuardLen)
			if _, rerr := f.ReadAt(guardBuf, guardStart); rerr == nil && agent.FNV1a64(guardBuf) == cached.GuardHash {
				stateOK := true
				if hooks.RestoreState != nil {
					if rerr := hooks.RestoreState(cached.StateJSON); rerr != nil {
						stateOK = false
					}
				}
				if stateOK {
					if _, serr := f.Seek(cached.ResumeOffset, io.SeekStart); serr == nil {
						startOffset = cached.ResumeOffset
						records = make([]agent.UsageRecord, len(cached.Records))
						copy(records, cached.Records)
					}
				}
			}
		}
	}

	br := bufio.NewReaderSize(f, 64*1024)
	offset := startOffset
	resumeOffset := startOffset
	hasUnterminatedTrailing := false

	var lineBuf []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		lineBuf = lineBuf[:0]
		oversized := false
		hasNewline := false
		var lineBytes int64

		for {
			chunk, rerr := br.ReadSlice('\n')
			n := int64(len(chunk))
			lineBytes += n
			if !oversized {
				if len(lineBuf)+len(chunk) > usageMaxLineBytes {
					oversized = true
					lineBuf = lineBuf[:0]
				} else {
					lineBuf = append(lineBuf, chunk...)
				}
			}
			if rerr == nil {
				hasNewline = true
				break
			}
			if rerr == bufio.ErrBufferFull {
				continue
			}
			if rerr == io.EOF {
				break
			}
			return records, rerr
		}

		if lineBytes == 0 {
			break
		}

		offset += lineBytes
		if hasNewline {
			resumeOffset = offset
		} else {
			hasUnterminatedTrailing = true
		}

		if !oversized && hooks.ParseLine != nil {
			trimmed := bytes.TrimRight(lineBuf, "\r\n")
			if len(bytes.TrimSpace(trimmed)) > 0 {
				emitted, stop := hooks.ParseLine(trimmed)
				if len(emitted) > 0 {
					records = append(records, emitted...)
				}
				if stop {
					break
				}
			}
		}

		if !hasNewline {
			break
		}
	}

	if cache != nil {
		cacheResumeOffset := resumeOffset
		if hasUnterminatedTrailing {
			// Do not resume from mid-file if an unterminated trailing line was
			// parsed into records; unchanged files still hit size+mtime cache,
			// and any append triggers a clean full reparse.
			cacheResumeOffset = 0
		}
		var (
			guardLen  int
			guardHash uint64
		)
		if cacheResumeOffset > 0 {
			guardLen = usageGuardBytes
			if cacheResumeOffset < int64(guardLen) {
				guardLen = int(cacheResumeOffset)
			}
			guardBuf := make([]byte, guardLen)
			if _, rerr := f.ReadAt(guardBuf, cacheResumeOffset-int64(guardLen)); rerr == nil {
				guardHash = agent.FNV1a64(guardBuf)
			} else {
				guardLen = 0
				cacheResumeOffset = 0
			}
		}
		var stateJSON []byte
		if hooks.SaveState != nil {
			if data, serr := hooks.SaveState(); serr == nil {
				stateJSON = data
			}
		}
		cachedCopy := make([]agent.UsageRecord, len(records))
		copy(cachedCopy, records)
		cache.Put(path, agent.CachedFileUsage{
			Size:         size,
			MtimeNs:      mtimeNs,
			ResumeOffset: cacheResumeOffset,
			GuardLen:     guardLen,
			GuardHash:    guardHash,
			StateJSON:    stateJSON,
			Records:      cachedCopy,
		})
	}

	return records, nil
}

func resolveUsageHome(opts agent.UsageScanOptions) (string, error) {
	if strings.TrimSpace(opts.HomeDir) != "" {
		return opts.HomeDir, nil
	}
	return os.UserHomeDir()
}

func expandUsageHomePath(p, home string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

func dedupeUsageDirs(dirs []string) []string {
	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		cleaned := filepath.Clean(d)
		key := cleaned
		if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && resolved != "" {
			key = resolved
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func usageFileTooOld(info fs.FileInfo, sinceMs int64) bool {
	if sinceMs <= 0 || info == nil {
		return false
	}
	cutoffMs := sinceMs - usageSinceGracePeriod.Milliseconds()
	return info.ModTime().UnixMilli() < cutoffMs
}

func finalizeUsageRecords(
	records []agent.UsageRecord,
	opts agent.UsageScanOptions,
	sourcePath string,
	seenDedupe map[string]struct{},
) []agent.UsageRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]agent.UsageRecord, 0, len(records))
	for _, rec := range records {
		if opts.SinceMs > 0 && rec.TimestampMs < opts.SinceMs {
			continue
		}
		if rec.DedupeKey != "" && seenDedupe != nil {
			if _, exists := seenDedupe[rec.DedupeKey]; exists {
				continue
			}
			seenDedupe[rec.DedupeKey] = struct{}{}
		}
		if rec.Profile == "" && opts.Profile != "" {
			rec.Profile = opts.Profile
		}
		if rec.SourcePath == "" && sourcePath != "" {
			rec.SourcePath = sourcePath
		}
		out = append(out, rec)
	}
	return out
}

func parseUsageTimestampMs(raw any) (int64, bool) {
	switch v := raw.(type) {
	case string:
		return parseUsageTimestampStringMs(v)
	case float64:
		return normalizeUnixTimestampMs(v)
	case int64:
		return normalizeUnixTimestampMs(float64(v))
	case int:
		return normalizeUnixTimestampMs(float64(v))
	default:
		return 0, false
	}
}

func parseUsageTimestampStringMs(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), true
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return normalizeUnixTimestampMs(f)
	}
	return 0, false
}

func normalizeUnixTimestampMs(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, false
	}
	switch {
	case v > 1e17: // nanoseconds
		return int64(v / 1e6), true
	case v > 1e14: // microseconds
		return int64(v / 1e3), true
	case v > 1e11: // milliseconds
		return int64(v), true
	default: // seconds
		return int64(v * 1000), true
	}
}

func usageTokenInt64(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	return int64(math.Trunc(v))
}

func usageFloatPtrIfNonNeg(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 {
		return nil
	}
	c := *v
	return &c
}

func usageFloatPtrIfPos(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 {
		return nil
	}
	c := *v
	return &c
}
