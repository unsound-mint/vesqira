// Package server exposes the catalog over a small JSON API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/unsound-mint/vesqira/services/api/internal/discussion"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/pushed"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type HeaderReader interface {
	Header(ctx context.Context, path string) (json.RawMessage, error)
}

type Server struct {
	pictures  sync.Map         // sha of an attachment or figure preview -> cleanedPicture (reports.go); the zero value is ready
	corrPaths sync.Map         // corrPathRef (host, resolved path) -> dataset id, for attaching corrections to worker requests (corrections.go)
	Roots     sourceroot.Roots // folders that may be read; grows when the owner opens a folder (use roots())
	Store     *catalog.Store
	Worker    HeaderReader
	Planes    PlaneService // persistent plane worker; nil answers 503
	// Load is the workload governor shared with Planes (worker.Persistent.Load): analysis programs take a heavy slot from it, and
	// GET /api/workload reports it. Nil runs analyses ungoverned and reports nothing.
	Load      *worker.Load
	Cache     *PlaneCache // optional; nil disables caching
	Boards    *boards.Store
	Runs      *runs.Store
	HostDB    *hosts.Store // remote hosts; nil disables them
	Remote    *hosts.Manager
	Jobs      *jobs.Store
	Sweeps    *sweeps.Store    // parameter sweeps (ADR 0048); nil disables their routes
	Transfers *transfers.Store // the transfer queue (ADR 0051); nil disables the routes
	Derived   *derived.Store
	Masks     *masks.Store   // typed masks (ADR 0057); nil disables their routes
	Offline   *offline.Store // offline copies of bounded replies (ADR 0059); nil disables them

	OfflineProbeAfter time.Duration // zero means offlineProbeAfter; tests shorten it
	offlineRun        offlineRunner // the one running preparation (offline_prepare.go)
	ManagedDir        string
	// CacheDir holds rebuildable sidecars (the index of a large ROHSA .dat); never next to the data. Empty disables them.
	CacheDir   string
	Analysis   *AnalysisConfig
	Audit      *audit.Store  // nil records nothing
	Notes      *notify.Store // events for the owner (jobs ended, results imported); nil records nothing and disables the routes
	Regions    *regions.Store
	Subsets    *subsets.Store    // virtual subsets (ADR 0063); nil disables their routes
	subsetMu   sync.Mutex        // one subset is written as a file at a time (subsets.go)
	History    *history.Store    // the viewer's exploration history (ADR 0057); nil disables the routes
	Discussion *discussion.Store // members' comments on figures and reports (ADR 0050 slice 3); nil disables the routes
	SSHConfig  string            // the user's ssh_config, only read to offer host aliases; empty offers none
	Hints      hintSource        // overrides the catalog for the runs list's input hints (tests count calls)
	WebDist    string            // built web app served at / on this (loopback) listener; empty serves the API only
	Reports    *reports.Store    // nil disables reports
	// PublishKeyFile holds the publisher key for the publish server (0600, beside the catalog); never in the catalog.
	PublishKeyFile string

	// Push to a group server (ADR 0066). On the group server Pushed keeps what linked instances push (nil disables the routes).
	// On a local instance Outbox is the link and the queue, GroupKeyFile the 0600 file beside the catalog that holds the push
	// token (never the catalog), and GroupHTTP the client that reaches the server (nil: a default with certificate checks and no
	// redirects; tests hand in one that trusts their test server).
	Pushed       *pushed.Store
	Outbox       *outbox.Store
	GroupKeyFile string
	GroupHTTP    *http.Client
	outboxState
	pushGuard pushGuard // decode slots and refused-upload counts of the push routes (push_group.go)

	syncState

	sacct sacctMemory // what each host's sacct accepts (sacctcaps.go)

	Projects *projects.Store // opened project folders; nil disables the project routes

	rootsOnce sync.Once // loads the opened projects' folders into projRoots on first use
	rootsMu   sync.RWMutex
	projRoots []string
	projMu    sync.Mutex
	projScans map[int64]*scanState
	laneOnce  sync.Once
	heavy     chan struct{} // one FITS header registration at a time, across projects

	importMu    sync.Mutex
	importInvs  map[int64]*importInv // the last import-preview walk of each project (projects_import.go)
	importBusy  sync.Map             // project id: an import apply is running
	importWalks sync.Map             // project id: *sync.Mutex held across an import-preview walk

	// EditorEnabled mounts the text editor routes (--enable-editor, ADR 0029). Off by default.
	EditorEnabled bool
	editMu        sync.Mutex            // one editor save at a time
	hostEditMu    sync.Mutex            // guards hostEditLocks
	hostEditLocks map[int64]*sync.Mutex // one editor save per host at a time (a save waits on ssh, so it does not share editMu)

	// TerminalEnabled mounts the integrated terminal routes (--enable-terminal, ADR 0029). Off by default.
	TerminalEnabled bool
	TerminalShell   []string       // argv of the local shell; empty runs the user's login shell ($SHELL -l). Tests set it.
	TerminalLimits  TerminalLimits // zero values take the defaults
	terms           termRegistry

	// VPNEnabled mounts the VPN routes (--enable-vpn, ADR 0029 step 3) and lets a host carry a VPN command. Off by default.
	VPNEnabled bool
	VPNTimeout time.Duration // how long connect waits for the host to answer, and a disconnect command may run; zero means hosts.DefaultVPNRun
	VPNPoll    time.Duration // how often connect asks the host check meanwhile; zero means three seconds
	VPNLogDir  string        // where a started VPN client writes its output (0600 files); empty means a per-user folder under the temp dir
	vpn        vpnRuns
	reach      reachability                                                                      // whether each host last answered (vpn.go)
	pingHook   func(ctx context.Context, h hosts.Host, req map[string]any) (worker.Reply, error) // test stand-in for the host check

	// MultiUser, when its PublicOrigin is set, makes this listener the group server (--multi-user, ADR 0050 slice 2; multiuser.go).
	MultiUser MultiUser

	// onRoute, when set, is told of every declaration s.route receives (tests list the routes with it).
	onRoute func(pattern string, perm authz.Permission)

	Chooser Chooser // opens the desktop file chooser; nil tries the portal, then zenity or kdialog

	records      sync.WaitGroup // background run-record writes in flight (hostwrite.go)
	recordLocks  sync.Map       // job id -> *sync.Mutex: one record write per job at a time
	sweepMu      sync.Mutex     // one run of the sweep scheduler or cancel at a time (sweeps.go)
	sweepRuns    sync.WaitGroup // scheduler runs in flight
	sweepBoardMu sync.Mutex     // one board per sweep
	imports      sync.WaitGroup // automatic imports of finished jobs in flight (jobwatch.go)
	disk         hostDisk       // cached storage looks and measured folder sizes per host (hoststorage.go)
	xfer         transferRunner // the running transfers and the hosts' rsync probes (transfers.go)

	// FullHashCap is the largest file a full SHA-256 is computed for (0 means 16 GiB). Full hashes run in the background, one
	// at a time, and can be cancelled (identity.go, ADR 0053).
	FullHashCap int64
	hashing     hashRegistry
	hashes      sync.WaitGroup // full hashes in flight

	batch    batchLane     // the group server's per-account batch lane (batchlane.go)
	support  supportStore  // support bundle previews waiting to be saved (support.go)
	choosing atomic.Bool   // a chooser dialog is open
	progress progressCache // what job logs said about progress, for board panels that follow a job
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.route(mux, "GET /api/capabilities", authz.CapabilitiesRead, s.capabilities)
	s.route(mux, "GET /api/workload", authz.CapabilitiesRead, s.workload)
	s.route(mux, "POST /api/support/preview", authz.SupportExport, s.supportPreview)
	s.route(mux, "GET /api/support/bundle/{id}", authz.SupportExport, s.supportDownload)
	s.route(mux, "GET /api/roots", authz.FilesRead, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"roots": s.roots(wsOf(r))})
	})
	s.route(mux, "GET /api/files/tree", authz.FilesRead, s.fileTree)
	s.route(mux, "GET /api/datasets", authz.DatasetRead, s.list)
	s.route(mux, "GET /api/datasets/availability", authz.DatasetRead, s.availability)
	s.route(mux, "GET /api/datasets/newer", authz.DatasetRead, s.newerResults)
	s.route(mux, "GET /api/datasets/{id}", authz.DatasetRead, s.get)
	s.route(mux, "GET /api/datasets/{id}/locations", authz.DatasetRead, s.listLocations)
	s.route(mux, "POST /api/datasets/{id}/locations", authz.DatasetWrite, s.addLocation)
	s.route(mux, "POST /api/datasets/{id}/locations/{lid}/delete", authz.DatasetWrite, s.removeLocation)
	s.route(mux, "POST /api/datasets/{id}/locations/{lid}/fingerprint", authz.DatasetWrite, s.fingerprintLocation)
	s.route(mux, "POST /api/datasets", authz.DatasetWrite, s.register)
	s.route(mux, "POST /api/catalogs", authz.DatasetWrite, s.registerCatalog)
	s.route(mux, "GET /api/datasets/{id}/catalog/report", authz.DatasetRead, s.catalogReport)
	s.route(mux, "GET /api/datasets/{id}/catalog/rows", authz.DatasetRead, s.catalogRows)
	s.route(mux, "GET /api/datasets/{id}/catalog/positions", authz.DatasetRead, s.catalogPositions)
	s.route(mux, "PUT /api/datasets/{id}/catalog/declaration", authz.DatasetWrite, s.putCatalogDeclaration)
	s.route(mux, "DELETE /api/datasets/{id}/catalog/declaration", authz.DatasetWrite, s.deleteCatalogDeclaration)
	s.route(mux, "GET /api/datasets/{id}/impact", authz.LifecycleDelete, s.lifecycleImpact("dataset"))
	s.route(mux, "POST /api/datasets/{id}/archive", authz.LifecycleArchive, s.lifecycleArchive("dataset", true))
	s.route(mux, "POST /api/datasets/{id}/restore", authz.LifecycleArchive, s.lifecycleArchive("dataset", false))
	s.route(mux, "POST /api/datasets/{id}/delete", authz.LifecycleDelete, s.lifecycleDelete("dataset"))
	s.route(mux, "GET /api/fs/complete", authz.FilesRead, s.completeLocal)
	s.route(mux, "POST /api/fs/choose", authz.LocalOnly, s.choose)
	if s.HostDB != nil && s.Remote != nil {
		s.route(mux, "GET /api/hosts", authz.ClusterRead, s.listHosts)
		s.route(mux, "GET /api/hosts/ssh-config", authz.LocalOnly, s.sshConfigHosts)
		s.route(mux, "GET /api/hosts/browse", authz.ClusterRead, s.browseHost)
		s.route(mux, "POST /api/hosts", authz.ClusterAdmin, s.createHost)
		s.route(mux, "POST /api/hosts/{id}/check", authz.ClusterRead, s.checkHost)
		if s.VPNEnabled {
			s.route(mux, "POST /api/hosts/{id}/vpn", authz.LocalOnly, s.hostVPN)
			s.route(mux, "POST /api/hosts/{id}/vpn-config", authz.LocalOnly, s.setHostVPN)
		}
		s.route(mux, "POST /api/hosts/{id}/delete", authz.ClusterAdmin, s.deleteHost)
		s.route(mux, "GET /api/hosts/{id}/list", authz.ClusterRead, s.listRemote)
		s.route(mux, "GET /api/hosts/{id}/complete", authz.ClusterRead, s.completeRemote)
		s.route(mux, "POST /api/hosts/{id}/register", authz.ClusterUse, s.registerRemote)
		s.route(mux, "GET /api/hosts/{id}/text", authz.ClusterRead, s.hostText)
		s.route(mux, "GET /api/hosts/{id}/storage", authz.ClusterRead, s.hostStorage(false))
		s.route(mux, "POST /api/hosts/{id}/storage/refresh", authz.ClusterRead, s.hostStorage(true))
		s.route(mux, "GET /api/hosts/{id}/folders", authz.ClusterRead, s.hostFolders)
		s.route(mux, "POST /api/hosts/{id}/folders/size", authz.ClusterRead, s.measureFolder)
		if s.Transfers != nil {
			s.route(mux, "GET /api/transfers", authz.ClusterRead, s.listTransfers)
			s.route(mux, "POST /api/transfers", authz.ClusterUse, s.createTransfer)
			s.route(mux, "POST /api/transfers/{id}/cancel", authz.ClusterUse, s.cancelTransfer)
			s.route(mux, "POST /api/transfers/{id}/retry", authz.ClusterUse, s.retryTransfer)
			s.route(mux, "POST /api/transfers/{id}/resolve", authz.ClusterUse, s.resolveTransfer)
			s.route(mux, "POST /api/transfers/{id}/remove", authz.ClusterUse, s.removeTransfer)
			s.route(mux, "POST /api/transfers/{id}/register", authz.ClusterUse, s.registerTransfer)
		}
		if s.Jobs != nil {
			s.route(mux, "GET /api/jobs", authz.JobRead, s.listJobs)
			s.route(mux, "GET /api/rohsa/sizing", authz.JobRead, s.rohsaSizing)
			s.route(mux, "POST /api/jobs", authz.JobSubmit, s.createJob)
			s.route(mux, "GET /api/jobs/{id}", authz.JobRead, s.getJob)
			s.route(mux, "POST /api/jobs/{id}/script", authz.JobSubmit, s.editJobScript)
			s.route(mux, "POST /api/jobs/{id}/submit", authz.JobSubmit, s.submitJob)
			s.route(mux, "POST /api/jobs/{id}/refresh", authz.JobAccess, s.refreshJob)
			s.route(mux, "POST /api/jobs/{id}/reopen", authz.JobSubmit, s.reopenJob)
			s.route(mux, "POST /api/jobs/{id}/cancel", authz.JobSubmit, s.cancelJob)
			s.route(mux, "GET /api/jobs/{id}/log", authz.JobAccess, s.jobLog)
			s.route(mux, "GET /api/jobs/{id}/rerun-check", authz.JobRead, s.getRerunCheck)
			if s.Runs != nil && s.Store != nil {
				s.route(mux, "GET /api/runs/{id}/rerun-on-input", authz.JobRead, s.previewInputRerun)
				s.route(mux, "POST /api/runs/{id}/rerun-on-input", authz.JobSubmit, s.createInputRerun)
			}
			s.route(mux, "POST /api/jobs/{id}/register", authz.JobAccess, s.registerJobOutputs)
			s.route(mux, "GET /api/jobs/{id}/result", authz.JobAccess, s.jobResult)
			if s.Sweeps != nil {
				s.route(mux, "POST /api/sweeps/preview", authz.SweepSubmit, s.previewSweep)
				s.route(mux, "GET /api/sweeps", authz.SweepRead, s.listSweeps)
				s.route(mux, "POST /api/sweeps", authz.SweepSubmit, s.createSweep)
				s.route(mux, "GET /api/sweeps/{id}", authz.SweepRead, s.getSweep)
				s.route(mux, "GET /api/sweeps/{id}/review", authz.SweepRead, s.reviewSweep)
				s.route(mux, "POST /api/sweeps/{id}/submit", authz.SweepSubmit, s.submitSweep)
				s.route(mux, "POST /api/sweeps/{id}/cancel", authz.SweepSubmit, s.cancelSweep)
				s.route(mux, "GET /api/sweeps/{id}/grid", authz.SweepRead, s.sweepGrid)
				if s.Boards != nil {
					s.route(mux, "POST /api/sweeps/{id}/board", authz.SweepSubmit, s.boardFromSweep)
				}
			}
		}
	}
	if s.analysisDeps() {
		s.route(mux, "POST /api/analysis", authz.AnalysisRun, s.analyze)
		s.route(mux, "GET /api/datasets/{id}/derived", authz.DatasetRead, s.getDerived)
		if s.Masks != nil {
			s.route(mux, "GET /api/datasets/{id}/masks", authz.DatasetRead, s.listMasks)
			s.route(mux, "POST /api/datasets/{id}/masks", authz.DatasetWrite, s.createMask)
			s.route(mux, "POST /api/masks/{id}/delete", authz.DatasetWrite, s.deleteMask)
			s.route(mux, "GET /api/masks/{id}/plane", authz.DatasetRead, s.maskPlane)
		}
		s.route(mux, "POST /api/analysis/relative-orientation", authz.AnalysisRun, s.relativeOrientation)
		s.route(mux, "GET /api/analysis/relative-orientation/presets", authz.AnalysisRun, s.relativeOrientationPresets)
		s.route(mux, "POST /api/analysis/healpix-sample", authz.AnalysisRun, s.healpixSample)
		if s.Runs != nil {
			s.route(mux, "POST /api/datasets/{id}/rohsa/sigmav", authz.AnalysisRun, s.rohsaSigmav)
			s.route(mux, "POST /api/datasets/{id}/rohsa/sigmav/cell", authz.AnalysisRun, s.rohsaCellMask)
			s.route(mux, "POST /api/datasets/{id}/rohsa/class-maps", authz.AnalysisRun, s.rohsaClassMaps)
		}
		s.route(mux, "GET /api/datasets/{id}/relative-orientation.csv", authz.DatasetRead, s.relativeOrientationTable)
		if s.HostDB != nil && s.Remote != nil && s.Jobs != nil {
			s.route(mux, "POST /api/analysis/job", authz.JobSubmit, s.analysisJob)
		}
	}
	if s.Runs != nil {
		s.route(mux, "POST /api/runs/import", authz.RunWrite, s.importRun)
		s.route(mux, "GET /api/runs", authz.RunRead, s.listRuns)
		s.route(mux, "GET /api/run-views", authz.RunRead, s.listRunViews)
		s.route(mux, "POST /api/run-views", authz.RunWrite, s.saveRunView)
		s.route(mux, "POST /api/run-views/{id}/delete", authz.RunWrite, s.deleteRunView)
		s.route(mux, "POST /api/runs/convert-values", authz.RunRead, s.convertRunValues)
		s.route(mux, "GET /api/runs/{id}", authz.RunRead, s.getRun)
		s.route(mux, "GET /api/runs/{id}/rerun", authz.RunRead, s.rerunRun)
		s.route(mux, "POST /api/runs/{id}/review", authz.RunWrite, s.setRunReview)
		s.route(mux, "PUT /api/runs/{id}/controls", authz.RunWrite, s.setRunControls)
		s.route(mux, "GET /api/runs/{id}/history", authz.RunRead, s.runHistory)
		s.route(mux, "GET /api/runs/{id}/impact", authz.LifecycleDelete, s.lifecycleImpact("run"))
		s.route(mux, "POST /api/runs/{id}/archive", authz.LifecycleArchive, s.lifecycleArchive("run", true))
		s.route(mux, "POST /api/runs/{id}/restore", authz.LifecycleArchive, s.lifecycleArchive("run", false))
		s.route(mux, "POST /api/runs/{id}/delete", authz.LifecycleDelete, s.lifecycleDelete("run"))
	}
	if s.Boards != nil {
		s.route(mux, "POST /api/figures", authz.FigureWrite, s.createFigure)
		s.route(mux, "POST /api/figures/runs", authz.FigureWrite, s.createRunFigure)
		s.route(mux, "GET /api/figures", authz.FigureRead, s.listFigures)
		s.route(mux, "GET /api/figures/{id}/preview", authz.FigureRead, s.figurePreview)
		s.route(mux, "GET /api/figures/{id}/download", authz.FigureRead, s.figureDownload)
		s.route(mux, "GET /api/figures/{id}/redraw-check", authz.FigureRead, s.redrawCheck)
		s.route(mux, "POST /api/figures/{id}/text", authz.FigureWrite, s.figureText)
		s.route(mux, "GET /api/figures/{id}/impact", authz.LifecycleDelete, s.lifecycleImpact("figure"))
		s.route(mux, "POST /api/figures/{id}/archive", authz.LifecycleArchive, s.lifecycleArchive("figure", true))
		s.route(mux, "POST /api/figures/{id}/restore", authz.LifecycleArchive, s.lifecycleArchive("figure", false))
		s.route(mux, "POST /api/figures/{id}/delete", authz.LifecycleDelete, s.lifecycleDelete("figure"))
		s.route(mux, "GET /api/boards/{id}/impact", authz.LifecycleDelete, s.lifecycleImpact("board"))
		s.route(mux, "POST /api/boards/{id}/archive", authz.LifecycleArchive, s.lifecycleArchive("board", true))
		s.route(mux, "POST /api/boards/{id}/restore", authz.LifecycleArchive, s.lifecycleArchive("board", false))
		s.route(mux, "POST /api/boards/{id}/delete", authz.LifecycleDelete, s.lifecycleDelete("board"))
		s.route(mux, "GET /api/boards", authz.BoardRead, s.listBoards)
		s.route(mux, "POST /api/boards", authz.BoardWrite, s.createBoard)
		s.route(mux, "GET /api/boards/{id}", authz.BoardRead, s.getBoard)
		s.route(mux, "POST /api/boards/{id}/items", authz.BoardWrite, s.boardAdd)
		s.route(mux, "POST /api/boards/{id}/items/{pos}/remove", authz.BoardWrite, s.boardItem("remove"))
		s.route(mux, "POST /api/boards/{id}/items/{pos}/move", authz.BoardWrite, s.boardItem("move"))
		s.route(mux, "POST /api/boards/{id}/items/{pos}/note", authz.BoardWrite, s.boardItem("note"))
		s.route(mux, "POST /api/boards/{id}/items/{pos}/pin", authz.BoardWrite, s.boardPin)
		s.route(mux, "POST /api/boards/{id}/layout", authz.BoardWrite, s.boardLayout)
		s.route(mux, "GET /api/boards/{id}/preflight", authz.ReviewPublish, s.preflight)
		s.route(mux, "POST /api/boards/{id}/publish", authz.ReviewPublish, s.publish)
		s.route(mux, "GET /api/boards/{id}/snapshots", authz.ReviewRead, s.snapshots)
		s.route(mux, "POST /api/snapshots/{id}/revoke", authz.ReviewPublish, s.revoke)
		s.route(mux, "GET /api/snapshots/{id}/comments", authz.ReviewRead, s.snapshotComments)
		s.route(mux, "POST /api/snapshots/{id}/comments", authz.ReviewPublish, s.ownerReply)
		s.route(mux, "POST /api/comments/{id}/resolve", authz.ReviewPublish, s.resolveComment)
	}
	if s.Projects != nil {
		s.route(mux, "POST /api/projects/open", authz.ProjectWrite, s.openProject)
		s.route(mux, "GET /api/projects", authz.ProjectRead, s.listProjects)
		s.route(mux, "GET /api/projects/{id}", authz.ProjectRead, s.getProject)
		s.route(mux, "POST /api/projects/{id}/scan", authz.ProjectWrite, s.scanProject)
		s.route(mux, "POST /api/projects/{id}/scan/cancel", authz.ProjectWrite, s.cancelScan)
		s.route(mux, "GET /api/projects/{id}/tree", authz.ProjectRead, s.projectTree)
		s.route(mux, "GET /api/projects/{id}/needs", authz.ProjectRead, s.projectNeeds)
		s.route(mux, "POST /api/projects/{id}/answers", authz.ProjectWrite, s.answerProject)
		s.route(mux, "POST /api/projects/{id}/project-file", authz.ProjectWrite, s.writeProject)
		s.route(mux, "GET /api/projects/{id}/import/rules", authz.ProjectRead, s.importRulesGet)
		s.route(mux, "PUT /api/projects/{id}/import/rules", authz.ProjectWrite, s.importRulesPut)
		s.route(mux, "POST /api/projects/{id}/import/rules/write", authz.ProjectWrite, s.importRulesWrite)
		s.route(mux, "POST /api/projects/{id}/import/preview", authz.ProjectWrite, s.importPreviewRoute)
		s.route(mux, "POST /api/projects/{id}/import/apply", authz.ProjectWrite, s.importApplyRoute)
	}
	if s.EditorEnabled {
		s.route(mux, "GET /api/files/text", authz.LocalOnly, s.getText)
		s.route(mux, "PUT /api/files/text", authz.LocalOnly, s.putText)
	}
	if s.TerminalEnabled {
		s.route(mux, "POST /api/terminal/sessions", authz.LocalOnly, s.openTerminalSession)
		s.route(mux, "GET /api/terminal", authz.LocalOnly, s.terminalSocket)
	}
	s.route(mux, "GET /api/datasets/{id}/header-cards", authz.DatasetRead, s.headerCards)
	s.route(mux, "GET /api/datasets/{id}/view", authz.DatasetRead, s.view)
	s.route(mux, "GET /api/datasets/{id}/plane", authz.DatasetRead, s.plane)
	s.route(mux, "GET /api/datasets/{id}/coord", authz.DatasetRead, s.coord)
	s.route(mux, "GET /api/datasets/{id}/conversions", authz.DatasetRead, s.conversions)
	s.route(mux, "GET /api/datasets/{id}/convert", authz.DatasetRead, s.convert)
	s.route(mux, "GET /api/datasets/{id}/spectrum", authz.DatasetRead, s.spectrum)
	s.route(mux, "GET /api/datasets/{id}/region-histogram", authz.DatasetRead, s.regionHistogram)
	s.route(mux, "GET /api/datasets/{id}/channel-grid", authz.DatasetRead, s.channelGrid)
	s.route(mux, "GET /api/datasets/{id}/slab", authz.DatasetRead, s.slab)
	s.route(mux, "POST /api/datasets/{id}/pv", authz.DatasetRead, s.pv)
	s.route(mux, "GET /api/datasets/{id}/rohsa", authz.DatasetRead, s.rohsaRuns)
	s.route(mux, "POST /api/datasets/{id}/rohsa/index", authz.DatasetWrite, s.rohsaIndex)
	s.route(mux, "GET /api/datasets/{id}/rohsa/components", authz.DatasetRead, s.rohsaComponents)
	s.route(mux, "GET /api/datasets/{id}/axial", authz.DatasetRead, s.axial)
	s.route(mux, "POST /api/datasets/{id}/overlay-geometry", authz.DatasetRead, s.overlayGeometry)
	s.route(mux, "POST /api/datasets/{id}/link", authz.DatasetRead, s.link)
	if s.Regions != nil {
		s.route(mux, "GET /api/datasets/{id}/regions", authz.DatasetRead, s.listRegions)
		s.route(mux, "POST /api/datasets/{id}/regions", authz.DatasetWrite, s.createRegion)
		s.route(mux, "GET /api/datasets/{id}/regions.reg", authz.DatasetRead, s.regionsDS9)
		s.route(mux, "POST /api/datasets/{id}/regions/import", authz.DatasetWrite, s.importRegions)
		s.route(mux, "POST /api/regions/{id}", authz.DatasetWrite, s.updateRegion)
		s.route(mux, "POST /api/regions/{id}/delete", authz.DatasetWrite, s.deleteRegion)
	}
	if s.Subsets != nil { // owner listener only. A subset is viewed through these five routes and no other: each reads through its window
		s.route(mux, "GET /api/subsets", authz.DatasetRead, s.listSubsets)
		s.route(mux, "GET /api/subsets/{sid}", authz.DatasetRead, s.getSubset)
		s.route(mux, "POST /api/datasets/{id}/subsets", authz.DatasetWrite, s.createSubset)
		s.route(mux, "POST /api/subsets/{sid}", authz.DatasetWrite, s.editSubset)
		s.route(mux, "POST /api/subsets/{sid}/repin", authz.DatasetWrite, s.repinSubset)
		s.route(mux, "POST /api/subsets/{sid}/materialise", authz.AnalysisRun, s.materialiseSubset)
		s.route(mux, "POST /api/subsets/{sid}/delete", authz.DatasetWrite, s.deleteSubset)
		s.route(mux, "GET /api/subsets/{sid}/view", authz.DatasetRead, s.view)
		s.route(mux, "GET /api/subsets/{sid}/header-cards", authz.DatasetRead, s.headerCards)
		s.route(mux, "GET /api/subsets/{sid}/plane", authz.DatasetRead, s.plane)
		s.route(mux, "GET /api/subsets/{sid}/coord", authz.DatasetRead, s.coord)
		s.route(mux, "GET /api/subsets/{sid}/spectrum", authz.DatasetRead, s.spectrum)
	}
	if s.Offline != nil { // owner listener only: the review listener has no route to an offline copy (ADR 0059)
		s.route(mux, "GET /api/offline", authz.LocalOnly, s.offlineOverview)
		s.route(mux, "POST /api/offline/plan", authz.LocalOnly, s.offlinePlanRoute)
		s.route(mux, "POST /api/offline/prepare", authz.LocalOnly, s.offlinePrepareRoute)
		s.route(mux, "GET /api/offline/prepare", authz.LocalOnly, s.offlinePrepareStatus)
		s.route(mux, "POST /api/offline/prepare/cancel", authz.LocalOnly, s.offlinePrepareCancel)
		s.route(mux, "PUT /api/offline/settings", authz.LocalOnly, s.offlineSettings)
		s.route(mux, "POST /api/offline/forget", authz.LocalOnly, s.offlineForget)
	}
	if s.History != nil {
		s.route(mux, "GET /api/history", authz.HistoryRead, s.listHistory)
		s.route(mux, "GET /api/history/{id}", authz.HistoryRead, s.getHistoryStep)
		s.route(mux, "POST /api/history", authz.HistoryWrite, s.addHistoryStep)
	}
	s.route(mux, "GET /api/compat", authz.DatasetRead, s.compatibility)
	if s.Audit != nil {
		s.route(mux, "GET /api/audit", authz.AuditRead, s.listAudit)
	}
	if s.Notes != nil {
		s.route(mux, "GET /api/notifications", authz.NotificationSelf, s.listNotifications)
		s.route(mux, "POST /api/notifications/read", authz.NotificationSelf, s.readNotifications)
	}
	s.route(mux, "GET /api/preview", authz.FilesRead, s.previewFile)
	s.route(mux, "POST /api/datasets/{id}/copy", authz.AnalysisRun, s.copyDataset)
	s.route(mux, "POST /api/datasets/{id}/nature", authz.DatasetWrite, s.setNature)
	s.route(mux, "POST /api/datasets/{id}/interpretation", authz.DatasetWrite, s.interpret)
	s.route(mux, "POST /api/datasets/{id}/unit", authz.DatasetWrite, s.declareUnit)
	s.route(mux, "GET /api/datasets/{id}/corrections", authz.DatasetRead, s.listCorrections)
	s.route(mux, "POST /api/datasets/{id}/corrections", authz.DatasetWrite, s.saveCorrection)
	s.route(mux, "POST /api/datasets/{id}/corrections/preview", authz.DatasetRead, s.previewCorrection)
	s.route(mux, "GET /api/datasets/{id}/corrections/sidecar", authz.DatasetRead, s.exportSidecar)
	s.route(mux, "POST /api/datasets/{id}/corrections/sidecar", authz.DatasetWrite, s.importSidecar)
	s.route(mux, "POST /api/datasets/{id}/corrections/sidecar/preview", authz.DatasetRead, s.previewSidecar)
	if s.Reports != nil && s.Boards != nil {
		s.reportRoutes(mux)
	}
	s.discussionRoutes(mux)
	if s.multiUser() {
		s.accountRoutes(mux)
		s.memberRoutes(mux)
	}
	if s.Pushed != nil {
		s.pushRoutes(mux) // group server only (groupServerOnly in route.go): absent in local mode
	}
	if s.Outbox != nil {
		s.outboxRoutes(mux) // local only (authz.LocalOnly): never mounted on the group server
	}
	if s.WebDist != "" {
		s.route(mux, "GET /", authz.Anonymous, staticApp(s.WebDist))
	}
	if s.multiUser() { // the group server: Host and Origin against --public-origin, and an account instead of the local owner
		return logRequests(s.groupOnly(s.withAccount(mux)))
	}
	return logRequests(localOnly(withLocalOwner(mux)))
}

// staticApp serves the built web app for the installed program, so one address is the whole application. Paths
// that are not files fall back to index.html (the app routes in the browser); /api is never answered from disk.
func staticApp(dist string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, 404, "not found")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if st, err := os.Stat(filepath.Join(dist, filepath.Clean("/"+r.URL.Path))); (err != nil || !st.Mode().IsRegular()) && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// localOnly rejects requests whose Host is not a loopback name, which blocks DNS-rebinding
// pages from reaching the local API, and rejects cross-site browser writes.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := lastColon(host); i >= 0 {
			host = host[:i]
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// A page on another site must not make the API read files or run the worker, even by GET.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		// A page on another origin must not write, whatever Sec-Fetch-Site a client sends or omits.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			}
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			if s[len(s)-1] == ']' {
				return -1
			}
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.List(wsOf(r))
	if err != nil {
		log.Printf("list: %v", err)
		fail(w, 500, "internal error")
		return
	}
	mode := archivedMode(r)
	shown := ds[:0]
	for _, d := range ds {
		if keepArchived(mode, d.Archived) {
			shown = append(shown, d)
		}
	}
	rows := make([]datasetRow, len(shown))
	at := map[int64]int{}
	for i, d := range shown {
		rows[i] = datasetRow{Dataset: d}
		at[d.ID] = i
	}
	if srcs, err := s.lc().MaskSources(wsOf(r)); err != nil {
		log.Printf("list: mask sources: %v", err) // the list is still right; the explorer just does not group these
	} else {
		for _, m := range srcs {
			if i, ok := at[m.DatasetID]; ok {
				for _, t := range m.Targets {
					rows[i].MaskSourceFor = append(rows[i].MaskSourceFor, maskSourceRef{ID: t.ID, Name: t.Label})
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"datasets": rows})
}

// datasetRow is a dataset in the list, with the datasets it is only the mask file of (ADR 0055, 0058). The list derives it from the
// masks' rows on every request: a file that is also used for anything else, or whose masks were forgotten, has none.
type datasetRow struct {
	catalog.Dataset
	MaskSourceFor []maskSourceRef `json:"maskSourceFor,omitempty"`
}

// availability says, for each registered file on this computer, whether it is still what was registered: available, modified
// (size or modification time differ, so the stored header may be out of date), missing, or outside the source roots. Files on
// other hosts are not contacted here (that would wait on the network and the VPN) and are reported as unchecked. Only datasets
// that are not simply available are listed.
func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.List(wsOf(r))
	if err != nil {
		log.Printf("availability: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := map[string]string{}
	for _, d := range ds {
		if d.HostID != 0 {
			continue
		}
		_, _, status, _ := s.authorize(r.Context(), d)
		if status != 0 && status != 409 && s.readableLocally(r.Context(), d) { // the registered file is gone but a verified copy here reads: the viewer works
			continue // (a file that changed is still said to have changed)
		}
		switch status {
		case 0:
		case 404:
			out[strconv.FormatInt(d.ID, 10)] = "missing"
		case 409:
			out[strconv.FormatInt(d.ID, 10)] = "modified"
		default:
			if _, err := os.Lstat(d.Path); errors.Is(err, fs.ErrNotExist) { // the roots check cannot resolve a path that is gone
				out[strconv.FormatInt(d.ID, 10)] = "missing"
			} else {
				out[strconv.FormatInt(d.ID, 10)] = "not_allowed"
			}
		}
	}
	writeJSON(w, 200, map[string]any{"problems": out, "note": "remote files are not checked here"})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	d, err := s.Store.Get(wsOf(r), id)
	if errors.Is(err, catalog.ErrNotFound) {
		fail(w, 404, "dataset not found")
		return
	}
	if err != nil {
		log.Printf("get: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, d)
}

// registerPath reads the header of an already-authorized file and records the reference.
//
// A file named like a table (.csv, .ecsv, .vot, ...) is a point catalog (ADR 0044). A FITS file with no image axes may be a
// table extension: the worker is asked, and only a table it can read becomes a catalog; otherwise it stays a FITS dataset
// exactly as before.
func (s *Server) registerPath(ctx context.Context, real string) (catalog.Dataset, int, string) {
	if inPartialFolder(real) {
		return catalog.Dataset{}, 422, "this file is part of an unfinished transfer and cannot be registered; it gets its real name when the transfer completes"
	}
	if hasCatalogExt(real) {
		return s.registerCatalogPath(ctx, real)
	}
	hdr, err := s.Worker.Header(ctx, real)
	if err != nil {
		return catalog.Dataset{}, 422, err.Error()
	}
	var axes struct {
		Naxis *int `json:"naxis"`
	}
	if json.Unmarshal(hdr, &axes) == nil && axes.Naxis != nil && *axes.Naxis == 0 && s.Planes != nil {
		if d, code, _ := s.registerCatalogPath(ctx, real); code == 0 {
			return d, 0, ""
		}
	}
	info, err := statFile(real)
	if err != nil {
		return catalog.Dataset{}, 404, "file not found"
	}
	dev, ino := fileIDOf(info)
	d, err := s.Store.Upsert(wsCtx(ctx), actorID(ctx), catalog.Dataset{Path: real, Name: filepath.Base(real), SizeBytes: info.Size(), ModifiedAt: info.ModTime(), FileDev: dev, FileIno: ino, Header: hdr})
	if errors.Is(err, catalog.ErrLocationExists) {
		return catalog.Dataset{}, 409, "this file is already another location of a dataset: remove that location first, or open that dataset"
	}
	if err != nil {
		log.Printf("register: %v", err)
		return catalog.Dataset{}, 500, "internal error"
	}
	return s.recordIdentity(ctx, d), 0, ""
}

// knownFile says whether a path on this computer already is a dataset's registered file or location: registering it again
// refreshes that dataset, so there is nothing to offer.
func (s *Server) knownFile(ws workspace.ID, real string) bool {
	_, found, _ := s.Store.LocationOwner(ws, 0, real)
	return found
}

// inPartialFolder reports whether a path is inside a folder where rsync keeps an unfinished transfer (ADR 0051).
func inPartialFolder(p string) bool {
	for _, c := range strings.Split(filepath.ToSlash(p), "/") {
		if c == hosts.PartialDir {
			return true
		}
	}
	return false
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Path string `json:"path"`
		// AsNew registers the file as its own dataset although a dataset already holds probably or verifiably the same bytes.
		AsNew bool `json:"asNew"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, "invalid request body")
		return
	}
	real, err := s.roots(wsOf(r)).Resolve(in.Path)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	if !in.AsNew && !s.knownFile(wsOf(r), real) {
		if m := s.sameContent(r.Context(), real); len(m) > 0 {
			failCode(w, 409, "same_content", "A dataset already holds this content. Add the file as another location of it, or register it as a separate dataset.", map[string]any{"matches": m})
			return
		}
	}
	d, code, msg := s.registerPath(waiting(r.Context()), real)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 201, d)
}
