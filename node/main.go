package main

import (
	"flag"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/coocood/badger"
	"github.com/coocood/badger/options"
	"github.com/ngaut/faketikv/tikv"
	"github.com/ngaut/log"
	"github.com/pingcap/kvproto/pkg/tikvpb"
	"google.golang.org/grpc"
)

var (
	pdAddr           = flag.String("pd-addr", "127.0.0.1:2379", "pd address")
	storeAddr        = flag.String("store-addr", "127.0.0.1:9191", "store address")
	httpAddr         = flag.String("http-addr", "127.0.0.1:9291", "http address")
	dbPath           = flag.String("db-path", "/tmp/badger", "Directory to store the data in. Should exist and be writable.")
	vlogPath         = flag.String("vlog-path", "", "Directory to store the value log in. can be the same as db-path.")
	valThreshold     = flag.Int("value-threshold", 20, "If value size >= this threshold, only store value offsets in tree.")
	regionSize       = flag.Int64("region-size", 96*1024*1024, "Average region size.")
	logLevel         = flag.String("L", "info", "log level")
	tableLoadingMode = flag.String("table-loading-mode", "memory-map", "How should LSM tree be accessed. (memory-map/load-to-ram)")
	maxTableSize     = flag.Int64("max-table-size", 64<<20, "Each table (or file) is at most this size.")
	numMemTables     = flag.Int("num-mem-tables", 3, "Maximum number of tables to keep in memory, before stalling.")
	numL0Table       = flag.Int("num-level-zero-tables", 3, "Maximum number of Level 0 tables before we start compacting.")
	syncWrites       = flag.Bool("sync-write", true, "Sync all writes to disk. Setting this to true would slow down data loading significantly.")
	logTrace         = flag.Uint("log-trace", 300, "Prints trace log if the request duration is greater than this value in milliseconds.")
	maxProcs         = flag.Int("max-procs", 0, "Max CPU cores to use, set 0 to use all CPU cores in the machine.")
)

var (
	gitHash = "None"
)

func main() {
	flag.Parse()
	runtime.GOMAXPROCS(*maxProcs)
	log.Info("gitHash:", gitHash)
	log.SetLevelByString(*logLevel)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	tikv.LogTraceMS = *logTrace
	go http.ListenAndServe(*httpAddr, nil)

	opts := badger.DefaultOptions
	opts.ValueThreshold = *valThreshold
	opts.Dir = *dbPath
	if *vlogPath != "" {
		opts.ValueDir = *vlogPath
	} else {
		opts.ValueDir = opts.Dir
	}
	if *tableLoadingMode == "memory-map" {
		opts.TableLoadingMode = options.MemoryMap
	}
	opts.ValueLogLoadingMode = options.FileIO
	opts.MaxTableSize = *maxTableSize
	opts.NumMemtables = *numMemTables
	opts.NumLevelZeroTables = *numL0Table
	opts.NumLevelZeroTablesStall = opts.NumLevelZeroTables + 5
	opts.SyncWrites = *syncWrites
	db, err := badger.Open(opts)
	if err != nil {
		log.Fatal(err)
	}
	regionOpts := tikv.RegionOptions{
		StoreAddr:  *storeAddr,
		PDAddr:     *pdAddr,
		RegionSize: *regionSize,
	}
	rm := tikv.NewRegionManager(db, regionOpts)
	store := tikv.NewMVCCStore(db, opts.Dir)
	tikvServer := tikv.NewServer(rm, store)

	grpcServer := grpc.NewServer()
	tikvpb.RegisterTikvServer(grpcServer, tikvServer)
	l, err := net.Listen("tcp", *storeAddr)
	if err != nil {
		log.Fatal(err)
	}
	handleSignal(grpcServer)
	err = grpcServer.Serve(l)
	if err != nil {
		log.Error(err)
	}
	tikvServer.Stop()
	log.Info("Server stopped.")
	err = store.Close()
	if err != nil {
		log.Error(err)
	} else {
		log.Info("Store closed.")
	}
	err = rm.Close()
	if err != nil {
		log.Error(err)
	} else {
		log.Info("RegionManager closed.")
	}
	err = db.Close()
	if err != nil {
		log.Error(err)
	} else {
		log.Info("DB closed.")
	}
}

func handleSignal(grpcServer *grpc.Server) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	go func() {
		sig := <-sigCh
		log.Infof("Got signal [%s] to exit.", sig)
		grpcServer.Stop()
	}()
}
