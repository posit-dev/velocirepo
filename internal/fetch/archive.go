package fetch

import (
	"net/http"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/dateutil"
)

// AttachArchive makes a fetch run record its archive in w: the responses of
// data that can't be fetched again, every target's options, and GitHub's
// snapshot refs. It keeps hooks that were already set.
func AttachArchive(opts *Options, w *archive.Writer) {
	wrap := opts.WrapTransport
	opts.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		if wrap != nil {
			base = wrap(base)
		}
		return &archive.Capture{Base: base, Writer: w}
	}

	onJob := opts.OnJob
	opts.OnJob = func(info JobInfo) {
		if onJob != nil {
			onJob(info)
		}
		w.AddJob(archiveJob(info))
	}

	onDone := opts.OnTargetDone
	opts.OnTargetDone = func(info JobInfo, src any) {
		if onDone != nil {
			onDone(info, src)
		}
		if s, ok := src.(interface{ SnapshotRefs() []int }); ok {
			w.SetSnapshotRefs(info.Source, info.Project, info.Target, s.SnapshotRefs())
		}
	}
}

func archiveJob(info JobInfo) archive.Job {
	j := archive.Job{
		Source:  info.Source,
		Project: info.Project,
		Target:  info.Target,
		Start:   map[string]string{"": dateutil.FormatDate(info.Options.StartDate)},
		End:     map[string]string{"": dateutil.FormatDate(info.Options.EndDate)},
		Zones:   info.Zones,
	}
	for series, w := range info.Options.Windows {
		j.End[series] = dateutil.FormatDate(w.End)
		if w.Start.IsZero() {
			j.Start[series] = ""
		} else {
			j.Start[series] = dateutil.FormatDate(w.Start)
		}
	}
	return j
}
