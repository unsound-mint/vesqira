// Package server exposes the catalog over a small JSON API.
package server

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
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
	Load       *worker.Load
	Cache      *PlaneCache // optional; nil disables caching
	Boards     *boards.Store
	Runs       *runs.Store
	HostDB     *hosts.Store // remote hosts; nil disables them
	Remote     *hosts.Manager
	Jobs       *jobs.Store
	Sweeps     *sweeps.Store    // parameter sweeps (ADR 0048); nil disables their routes
	Transfers  *transfers.Store // the transfer queue (ADR 0051); nil disables the routes
	Derived    *derived.Store
	Masks      *masks.Store   // typed masks (ADR 0057); nil disables their routes
	Offline    *offline.Store // offline copies of bounded replies (ADR 0059); nil disables them
	offlineRun offlineRunner  // the one running preparation (offline_prepare.go)
	ManagedDir string
	// CacheDir holds rebuildable sidecars (the index of a large ROHSA .dat); never next to the data. Empty disables them.
	CacheDir  string
	Analysis  *AnalysisConfig
	Audit     *audit.Store  // nil records nothing
	Notes     *notify.Store // events for the owner (jobs ended, results imported); nil records nothing and disables the routes
	Regions   *regions.Store
	Subsets   *subsets.Store // virtual subsets (ADR 0063); nil disables their routes
	subsetMu  sync.Mutex     // one subset is written as a file at a time (subsets.go)
	History   *history.Store // the viewer's exploration history (ADR 0057); nil disables the routes
	SSHConfig string         // the user's ssh_config, only read to offer host aliases; empty offers none
	Hints     hintSource     // overrides the catalog for the runs list's input hints (tests count calls)
	WebDist   string         // built web app served at / on this (loopback) listener; empty serves the API only
	Reports   *reports.Store // nil disables reports
	// PublishKeyFile holds the publisher key for the publish server (0600, beside the catalog); never in the catalog.
	PublishKeyFile string

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

	support  supportStore  // support bundle previews waiting to be saved (support.go)
	choosing atomic.Bool   // a chooser dialog is open
	progress progressCache // what job logs said about progress, for board panels that follow a job
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/capabilities", s.capabilities)
	mux.HandleFunc("GET /api/workload", s.workload)
	mux.HandleFunc("POST /api/support/preview", s.supportPreview)
	mux.HandleFunc("GET /api/support/bundle/{id}", s.supportDownload)
	mux.HandleFunc("GET /api/roots", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"roots": s.roots()}) })
	mux.HandleFunc("GET /api/files/tree", s.fileTree)
	mux.HandleFunc("GET /api/datasets", s.list)
	mux.HandleFunc("GET /api/datasets/availability", s.availability)
	mux.HandleFunc("GET /api/datasets/newer", s.newerResults)
	mux.HandleFunc("GET /api/datasets/{id}", s.get)
	mux.HandleFunc("GET /api/datasets/{id}/locations", s.listLocations)
	mux.HandleFunc("POST /api/datasets/{id}/locations", s.addLocation)
	mux.HandleFunc("POST /api/datasets/{id}/locations/{lid}/delete", s.removeLocation)
	mux.HandleFunc("POST /api/datasets/{id}/locations/{lid}/fingerprint", s.fingerprintLocation)
	mux.HandleFunc("POST /api/datasets", s.register)
	mux.HandleFunc("POST /api/catalogs", s.registerCatalog)
	mux.HandleFunc("GET /api/datasets/{id}/catalog/report", s.catalogReport)
	mux.HandleFunc("GET /api/datasets/{id}/catalog/rows", s.catalogRows)
	mux.HandleFunc("GET /api/datasets/{id}/catalog/positions", s.catalogPositions)
	mux.HandleFunc("PUT /api/datasets/{id}/catalog/declaration", s.putCatalogDeclaration)
	mux.HandleFunc("DELETE /api/datasets/{id}/catalog/declaration", s.deleteCatalogDeclaration)
	mux.HandleFunc("GET /api/datasets/{id}/impact", s.lifecycleImpact("dataset"))
	mux.HandleFunc("POST /api/datasets/{id}/archive", s.lifecycleArchive("dataset", true))
	mux.HandleFunc("POST /api/datasets/{id}/restore", s.lifecycleArchive("dataset", false))
	mux.HandleFunc("POST /api/datasets/{id}/delete", s.lifecycleDelete("dataset"))
	mux.HandleFunc("GET /api/fs/complete", s.completeLocal)
	mux.HandleFunc("POST /api/fs/choose", s.choose)
	if s.HostDB != nil && s.Remote != nil {
		mux.HandleFunc("GET /api/hosts", s.listHosts)
		mux.HandleFunc("GET /api/hosts/ssh-config", s.sshConfigHosts)
		mux.HandleFunc("GET /api/hosts/browse", s.browseHost)
		mux.HandleFunc("POST /api/hosts", s.createHost)
		mux.HandleFunc("POST /api/hosts/{id}/check", s.checkHost)
		if s.VPNEnabled {
			mux.HandleFunc("POST /api/hosts/{id}/vpn", s.hostVPN)
			mux.HandleFunc("POST /api/hosts/{id}/vpn-config", s.setHostVPN)
		}
		mux.HandleFunc("POST /api/hosts/{id}/delete", s.deleteHost)
		mux.HandleFunc("GET /api/hosts/{id}/list", s.listRemote)
		mux.HandleFunc("GET /api/hosts/{id}/complete", s.completeRemote)
		mux.HandleFunc("POST /api/hosts/{id}/register", s.registerRemote)
		mux.HandleFunc("GET /api/hosts/{id}/text", s.hostText)
		mux.HandleFunc("GET /api/hosts/{id}/storage", s.hostStorage(false))
		mux.HandleFunc("POST /api/hosts/{id}/storage/refresh", s.hostStorage(true))
		mux.HandleFunc("GET /api/hosts/{id}/folders", s.hostFolders)
		mux.HandleFunc("POST /api/hosts/{id}/folders/size", s.measureFolder)
		if s.Transfers != nil {
			mux.HandleFunc("GET /api/transfers", s.listTransfers)
			mux.HandleFunc("POST /api/transfers", s.createTransfer)
			mux.HandleFunc("POST /api/transfers/{id}/cancel", s.cancelTransfer)
			mux.HandleFunc("POST /api/transfers/{id}/retry", s.retryTransfer)
			mux.HandleFunc("POST /api/transfers/{id}/resolve", s.resolveTransfer)
			mux.HandleFunc("POST /api/transfers/{id}/remove", s.removeTransfer)
			mux.HandleFunc("POST /api/transfers/{id}/register", s.registerTransfer)
		}
		if s.Jobs != nil {
			mux.HandleFunc("GET /api/jobs", s.listJobs)
			mux.HandleFunc("GET /api/rohsa/sizing", s.rohsaSizing)
			mux.HandleFunc("POST /api/jobs", s.createJob)
			mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
			mux.HandleFunc("POST /api/jobs/{id}/script", s.editJobScript)
			mux.HandleFunc("POST /api/jobs/{id}/submit", s.submitJob)
			mux.HandleFunc("POST /api/jobs/{id}/refresh", s.refreshJob)
			mux.HandleFunc("POST /api/jobs/{id}/reopen", s.reopenJob)
			mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
			mux.HandleFunc("GET /api/jobs/{id}/log", s.jobLog)
			mux.HandleFunc("GET /api/jobs/{id}/rerun-check", s.getRerunCheck)
			mux.HandleFunc("POST /api/jobs/{id}/register", s.registerJobOutputs)
			mux.HandleFunc("GET /api/jobs/{id}/result", s.jobResult)
			if s.Sweeps != nil {
				mux.HandleFunc("POST /api/sweeps/preview", s.previewSweep)
				mux.HandleFunc("GET /api/sweeps", s.listSweeps)
				mux.HandleFunc("POST /api/sweeps", s.createSweep)
				mux.HandleFunc("GET /api/sweeps/{id}", s.getSweep)
				mux.HandleFunc("GET /api/sweeps/{id}/review", s.reviewSweep)
				mux.HandleFunc("POST /api/sweeps/{id}/submit", s.submitSweep)
				mux.HandleFunc("POST /api/sweeps/{id}/cancel", s.cancelSweep)
				mux.HandleFunc("GET /api/sweeps/{id}/grid", s.sweepGrid)
				if s.Boards != nil {
					mux.HandleFunc("POST /api/sweeps/{id}/board", s.boardFromSweep)
				}
			}
		}
	}
	if s.analysisDeps() {
		mux.HandleFunc("POST /api/analysis", s.analyze)
		mux.HandleFunc("GET /api/datasets/{id}/derived", s.getDerived)
		if s.Masks != nil {
			mux.HandleFunc("GET /api/datasets/{id}/masks", s.listMasks)
			mux.HandleFunc("POST /api/datasets/{id}/masks", s.createMask)
			mux.HandleFunc("POST /api/masks/{id}/delete", s.deleteMask)
			mux.HandleFunc("GET /api/masks/{id}/plane", s.maskPlane)
		}
		mux.HandleFunc("POST /api/analysis/relative-orientation", s.relativeOrientation)
		mux.HandleFunc("GET /api/datasets/{id}/relative-orientation.csv", s.relativeOrientationTable)
		if s.HostDB != nil && s.Remote != nil && s.Jobs != nil {
			mux.HandleFunc("POST /api/analysis/job", s.analysisJob)
		}
	}
	if s.Runs != nil {
		mux.HandleFunc("POST /api/runs/import", s.importRun)
		mux.HandleFunc("GET /api/runs", s.listRuns)
		mux.HandleFunc("GET /api/run-views", s.listRunViews)
		mux.HandleFunc("POST /api/run-views", s.saveRunView)
		mux.HandleFunc("POST /api/run-views/{id}/delete", s.deleteRunView)
		mux.HandleFunc("POST /api/runs/convert-values", s.convertRunValues)
		mux.HandleFunc("GET /api/runs/{id}", s.getRun)
		mux.HandleFunc("GET /api/runs/{id}/rerun", s.rerunRun)
		mux.HandleFunc("POST /api/runs/{id}/review", s.setRunReview)
		mux.HandleFunc("PUT /api/runs/{id}/controls", s.setRunControls)
		mux.HandleFunc("GET /api/runs/{id}/history", s.runHistory)
		mux.HandleFunc("GET /api/runs/{id}/impact", s.lifecycleImpact("run"))
		mux.HandleFunc("POST /api/runs/{id}/archive", s.lifecycleArchive("run", true))
		mux.HandleFunc("POST /api/runs/{id}/restore", s.lifecycleArchive("run", false))
		mux.HandleFunc("POST /api/runs/{id}/delete", s.lifecycleDelete("run"))
	}
	if s.Boards != nil {
		mux.HandleFunc("POST /api/figures", s.createFigure)
		mux.HandleFunc("GET /api/figures", s.listFigures)
		mux.HandleFunc("GET /api/figures/{id}/preview", s.figurePreview)
		mux.HandleFunc("GET /api/figures/{id}/download", s.figureDownload)
		mux.HandleFunc("GET /api/figures/{id}/redraw-check", s.redrawCheck)
		mux.HandleFunc("POST /api/figures/{id}/text", s.figureText)
		mux.HandleFunc("GET /api/figures/{id}/impact", s.lifecycleImpact("figure"))
		mux.HandleFunc("POST /api/figures/{id}/archive", s.lifecycleArchive("figure", true))
		mux.HandleFunc("POST /api/figures/{id}/restore", s.lifecycleArchive("figure", false))
		mux.HandleFunc("POST /api/figures/{id}/delete", s.lifecycleDelete("figure"))
		mux.HandleFunc("GET /api/boards/{id}/impact", s.lifecycleImpact("board"))
		mux.HandleFunc("POST /api/boards/{id}/archive", s.lifecycleArchive("board", true))
		mux.HandleFunc("POST /api/boards/{id}/restore", s.lifecycleArchive("board", false))
		mux.HandleFunc("POST /api/boards/{id}/delete", s.lifecycleDelete("board"))
		mux.HandleFunc("GET /api/boards", s.listBoards)
		mux.HandleFunc("POST /api/boards", s.createBoard)
		mux.HandleFunc("GET /api/boards/{id}", s.getBoard)
		mux.HandleFunc("POST /api/boards/{id}/items", s.boardAdd)
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/remove", s.boardItem("remove"))
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/move", s.boardItem("move"))
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/note", s.boardItem("note"))
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/pin", s.boardPin)
		mux.HandleFunc("POST /api/boards/{id}/layout", s.boardLayout)
		mux.HandleFunc("GET /api/boards/{id}/preflight", s.preflight)
		mux.HandleFunc("POST /api/boards/{id}/publish", s.publish)
		mux.HandleFunc("GET /api/boards/{id}/snapshots", s.snapshots)
		mux.HandleFunc("POST /api/snapshots/{id}/revoke", s.revoke)
		mux.HandleFunc("GET /api/snapshots/{id}/comments", s.snapshotComments)
		mux.HandleFunc("POST /api/snapshots/{id}/comments", s.ownerReply)
		mux.HandleFunc("POST /api/comments/{id}/resolve", s.resolveComment)
	}
	if s.Projects != nil {
		mux.HandleFunc("POST /api/projects/open", s.openProject)
		mux.HandleFunc("GET /api/projects", s.listProjects)
		mux.HandleFunc("GET /api/projects/{id}", s.getProject)
		mux.HandleFunc("POST /api/projects/{id}/scan", s.scanProject)
		mux.HandleFunc("POST /api/projects/{id}/scan/cancel", s.cancelScan)
		mux.HandleFunc("GET /api/projects/{id}/tree", s.projectTree)
		mux.HandleFunc("GET /api/projects/{id}/needs", s.projectNeeds)
		mux.HandleFunc("POST /api/projects/{id}/answers", s.answerProject)
		mux.HandleFunc("POST /api/projects/{id}/project-file", s.writeProject)
		mux.HandleFunc("GET /api/projects/{id}/import/rules", s.importRulesGet)
		mux.HandleFunc("PUT /api/projects/{id}/import/rules", s.importRulesPut)
		mux.HandleFunc("POST /api/projects/{id}/import/rules/write", s.importRulesWrite)
		mux.HandleFunc("POST /api/projects/{id}/import/preview", s.importPreviewRoute)
		mux.HandleFunc("POST /api/projects/{id}/import/apply", s.importApplyRoute)
	}
	if s.EditorEnabled {
		mux.HandleFunc("GET /api/files/text", s.getText)
		mux.HandleFunc("PUT /api/files/text", s.putText)
	}
	if s.TerminalEnabled {
		mux.HandleFunc("POST /api/terminal/sessions", s.openTerminalSession)
		mux.HandleFunc("GET /api/terminal", s.terminalSocket)
	}
	mux.HandleFunc("GET /api/datasets/{id}/header-cards", s.headerCards)
	mux.HandleFunc("GET /api/datasets/{id}/view", s.view)
	mux.HandleFunc("GET /api/datasets/{id}/plane", s.plane)
	mux.HandleFunc("GET /api/datasets/{id}/coord", s.coord)
	mux.HandleFunc("GET /api/datasets/{id}/conversions", s.conversions)
	mux.HandleFunc("GET /api/datasets/{id}/convert", s.convert)
	mux.HandleFunc("GET /api/datasets/{id}/spectrum", s.spectrum)
	mux.HandleFunc("GET /api/datasets/{id}/region-histogram", s.regionHistogram)
	mux.HandleFunc("GET /api/datasets/{id}/channel-grid", s.channelGrid)
	mux.HandleFunc("GET /api/datasets/{id}/slab", s.slab)
	mux.HandleFunc("POST /api/datasets/{id}/pv", s.pv)
	mux.HandleFunc("GET /api/datasets/{id}/rohsa", s.rohsaRuns)
	mux.HandleFunc("POST /api/datasets/{id}/rohsa/index", s.rohsaIndex)
	mux.HandleFunc("GET /api/datasets/{id}/rohsa/components", s.rohsaComponents)
	mux.HandleFunc("GET /api/datasets/{id}/axial", s.axial)
	mux.HandleFunc("POST /api/datasets/{id}/overlay-geometry", s.overlayGeometry)
	mux.HandleFunc("POST /api/datasets/{id}/link", s.link)
	if s.Regions != nil {
		mux.HandleFunc("GET /api/datasets/{id}/regions", s.listRegions)
		mux.HandleFunc("POST /api/datasets/{id}/regions", s.createRegion)
		mux.HandleFunc("GET /api/datasets/{id}/regions.reg", s.regionsDS9)
		mux.HandleFunc("POST /api/datasets/{id}/regions/import", s.importRegions)
		mux.HandleFunc("POST /api/regions/{id}", s.updateRegion)
		mux.HandleFunc("POST /api/regions/{id}/delete", s.deleteRegion)
	}
	if s.Subsets != nil { // owner listener only. A subset is viewed through these five routes and no other: each reads through its window
		mux.HandleFunc("GET /api/subsets", s.listSubsets)
		mux.HandleFunc("GET /api/subsets/{sid}", s.getSubset)
		mux.HandleFunc("POST /api/datasets/{id}/subsets", s.createSubset)
		mux.HandleFunc("POST /api/subsets/{sid}", s.editSubset)
		mux.HandleFunc("POST /api/subsets/{sid}/repin", s.repinSubset)
		mux.HandleFunc("POST /api/subsets/{sid}/materialise", s.materialiseSubset)
		mux.HandleFunc("POST /api/subsets/{sid}/delete", s.deleteSubset)
		mux.HandleFunc("GET /api/subsets/{sid}/view", s.view)
		mux.HandleFunc("GET /api/subsets/{sid}/header-cards", s.headerCards)
		mux.HandleFunc("GET /api/subsets/{sid}/plane", s.plane)
		mux.HandleFunc("GET /api/subsets/{sid}/coord", s.coord)
		mux.HandleFunc("GET /api/subsets/{sid}/spectrum", s.spectrum)
	}
	if s.Offline != nil { // owner listener only: the review listener has no route to an offline copy (ADR 0059)
		mux.HandleFunc("GET /api/offline", s.offlineOverview)
		mux.HandleFunc("POST /api/offline/plan", s.offlinePlanRoute)
		mux.HandleFunc("POST /api/offline/prepare", s.offlinePrepareRoute)
		mux.HandleFunc("GET /api/offline/prepare", s.offlinePrepareStatus)
		mux.HandleFunc("POST /api/offline/prepare/cancel", s.offlinePrepareCancel)
		mux.HandleFunc("PUT /api/offline/settings", s.offlineSettings)
		mux.HandleFunc("POST /api/offline/forget", s.offlineForget)
	}
	if s.History != nil {
		mux.HandleFunc("GET /api/history", s.listHistory)
		mux.HandleFunc("GET /api/history/{id}", s.getHistoryStep)
		mux.HandleFunc("POST /api/history", s.addHistoryStep)
	}
	mux.HandleFunc("GET /api/compat", s.compatibility)
	if s.Audit != nil {
		mux.HandleFunc("GET /api/audit", s.listAudit)
	}
	if s.Notes != nil {
		mux.HandleFunc("GET /api/notifications", s.listNotifications)
		mux.HandleFunc("POST /api/notifications/read", s.readNotifications)
	}
	mux.HandleFunc("GET /api/preview", s.previewFile)
	mux.HandleFunc("POST /api/datasets/{id}/copy", s.copyDataset)
	mux.HandleFunc("POST /api/datasets/{id}/nature", s.setNature)
	mux.HandleFunc("POST /api/datasets/{id}/interpretation", s.interpret)
	mux.HandleFunc("POST /api/datasets/{id}/unit", s.declareUnit)
	mux.HandleFunc("GET /api/datasets/{id}/corrections", s.listCorrections)
	mux.HandleFunc("POST /api/datasets/{id}/corrections", s.saveCorrection)
	mux.HandleFunc("POST /api/datasets/{id}/corrections/preview", s.previewCorrection)
	mux.HandleFunc("GET /api/datasets/{id}/corrections/sidecar", s.exportSidecar)
	mux.HandleFunc("POST /api/datasets/{id}/corrections/sidecar", s.importSidecar)
	mux.HandleFunc("POST /api/datasets/{id}/corrections/sidecar/preview", s.previewSidecar)
	if s.Reports != nil && s.Boards != nil {
		s.reportRoutes(mux)
	}
	if s.WebDist != "" {
		mux.Handle("GET /", staticApp(s.WebDist))
	}
	return logRequests(localOnly(mux))
}

// staticApp serves the built web app for the installed program, so one address is the whole application. Paths
// that are not files fall back to index.html (the app routes in the browser); /api is never answered from disk.
func staticApp(dist string) http.Handler {
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
	ds, err := s.Store.List()
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
	if srcs, err := s.lc().MaskSources(); err != nil {
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
	ds, err := s.Store.List()
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
	d, err := s.Store.Get(id)
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
	d, err := s.Store.Upsert(catalog.Dataset{Path: real, Name: filepath.Base(real), SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Header: hdr})
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
func (s *Server) knownFile(real string) bool {
	_, found, _ := s.Store.LocationOwner(0, real)
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
	real, err := s.roots().Resolve(in.Path)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	if !in.AsNew && !s.knownFile(real) {
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
