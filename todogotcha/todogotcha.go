package main

// TODO: エラーログの吐き方 error buffer 作って溜めてリザルトで吐く?
//     : 複数のgoroutineが同じロガーに同時にアクセスしてもlogがよろしくやってくれるのか調べてない
//     : 普通に考えたら裏で常に走りながらチャンネルで待ち受けてそうだけどどうだろう
//     : pkgのsrcみると大丈夫そう
// NOTE: flagに直接触れるのは init, main, に限定する
//     : 出来るだけファイル一枚で書いてみる
//     : ファイル一枚に詰めながら処理は独立するように気をつける
//     : initでフラグとstickyなデータの初期化を任せてflagのエラー処理を省く
//     : goroutineいっぱい使ってみたい

// TODO: Refactor, フラグにくっついてるflag.dataの初期化をinitからレシーバに切り出す

// TODO: Review, To simple

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Close wrapper for log.Print
func loggingFileClose(at string, f interface {
	Close() error
}) {
	if err := f.Close(); err != nil {
		log.Printf("%s:%v", at, err)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, `
Description
	1, Search from current directory recursively
	2, Create List from search files
	3, Output to file or os.Stdout(default)
`)
	fmt.Fprintf(os.Stderr, `
All flags
`)
	flag.PrintDefaults()
	os.Exit(1)
}

// Flags for pkg name sort
// TODO: Reconsider need for flags
type Flags struct {
	// Flags for Data
	root        *string
	suffix      *string
	keyword     *string
	fileList    *string
	dirList     *string
	separator   *string
	recursively *bool
	ignoreLong  *int

	// Flags for Output
	output  *string
	result  *bool
	force   *bool
	sort    *bool
	date    *bool
	verbose *bool

	trim  *bool
	lines *uint

	// TODO: Specify GOMAXPROCS. Maybe future delete this
	proc *int
	// NOTE: Countermove "too many open files"
	limit *uint

	// Sticky data
	data struct {
		dir            []string
		file           []string
		filetypes      []string
		outputFilePath string
	}
}

// For stringer, ALL flags
func (f Flags) String() string {
	tmp := fmt.Sprintln("ALL FLAGS")
	tmp += fmt.Sprintf("result=%v\n", *f.result)
	tmp += fmt.Sprintf("root=%v\n", *f.root)
	tmp += fmt.Sprintf("wrod=%v\n", *f.keyword)
	tmp += fmt.Sprintf("type=%v\n", *f.suffix)

	tmp += fmt.Sprintf("recursive=%v\n", *f.recursively)
	tmp += fmt.Sprintf("ignore-long=%v\n", *f.ignoreLong)
	tmp += fmt.Sprintf("sort=%v\n", *f.sort)
	tmp += fmt.Sprintf("date=%v\n", *f.date)
	tmp += fmt.Sprintf("force=%v\n", *f.force)

	tmp += fmt.Sprintf("output=%v\n", *f.output)

	tmp += fmt.Sprintf("dir=%v\n", *f.dirList)
	tmp += fmt.Sprintf("file=%v\n", *f.fileList)
	tmp += fmt.Sprintf("sep=%v\n", *f.separator)

	tmp += fmt.Sprintf("trim=%v\n", *flags.trim)
	tmp += fmt.Sprintf("lines=%v\n", *flags.lines)
	tmp += fmt.Sprintf("proc=%v\n", runtime.GOMAXPROCS(0))
	tmp += fmt.Sprintf("limit=%v\n", *flags.limit)
	tmp += fmt.Sprintf("verbose=%v\n", *flags.verbose)
	return tmp
	// NOTE: とりあえずこのままで
}

// NOTE: goはこの書き方でもファイル内限定のstaticっぽい扱い
var flags = Flags{
	root:    flag.String("root", "./", "search root"),
	suffix:  flag.String("type", ".go .txt", "search file types(suffix)"),
	keyword: flag.String("word", "TODO: ", "search word"),

	fileList:  flag.String("file", "", `EXAMPLE -file="/path/to/file;/another/one"`),
	dirList:   flag.String("dir", "", `EXAMPLE -dir="/path/to/dir/;/another/one/"`),
	separator: flag.String("sep", ";", "separator for flags(-dir -file)"),

	output: flag.String("out", "", "specify output to filepath"),
	force:  flag.Bool("force", false, "ignore override confirm [true:false]?"),

	recursively: flag.Bool("recursive", true, "recursive search from -root [true:false]?"),
	ignoreLong:  flag.Int("ignore-long", 1024, "specify number of chars for ignore too long line"),
	result:      flag.Bool("result", false, "output state [true:false]?"),
	sort:        flag.Bool("sort", false, "sort by filepath [true:false]?"),
	date:        flag.Bool("date", false, "EXAMPLE -date=true ...append date to output [true:false]?"),

	trim:  flag.Bool("trim", true, "trim the -word from output [true:false]?"),
	lines: flag.Uint("lines", 1, "number of lines for gather"),

	proc:    flag.Int("proc", 0, "GOMAXPROCS"),
	limit:   flag.Uint("limit", 512, "limit of goroutine, for limitation of file descriptor"),
	verbose: flag.Bool("verbose", false, "Output all log massages [true:false]?"),
}

// TODO: init Reconsider, フラグ処理を入れてみたけどinitである必要は?
//     : flagsの処理はレシーバに切り出してまとめるべき,たぶん
func init() {
	// Parse and Unknown flags check
	flag.Usage = usage
	flag.Parse()
	argsCheck()

	log.SetPrefix("todogatcha: ")
	if *flags.verbose {
		log.SetOutput(os.Stderr)
	} else {
		// is do not close
		devnul, err := os.Open(os.DevNull)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		log.SetOutput(devnul)
	}

	runtime.GOMAXPROCS(*flags.proc)

	if *flags.limit <= 1 {
		fmt.Fprintln(os.Stderr, "-limit is require 2 or more")
		os.Exit(1)
	}

	if *flags.lines == 0 {
		fmt.Fprintln(os.Stderr, "-lines is require 1 line or more")
		os.Exit(1)
	}

	if *flags.root != "" {
		pathtmp, err := filepath.Abs(*flags.root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "init:%v", err)
			os.Exit(1)
		}
		*flags.root = pathtmp
	}

	// For output filepath
	if *flags.output != "" {
		cleanpath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(*flags.output)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "init:%v", err)
			os.Exit(1)
		}
		// Check override to specify output file
		if _, errstat := os.Stat(cleanpath); errstat == nil && *flags.force == false {
			if !ask(fmt.Sprintf("Override? %v", cleanpath)) {
				os.Exit(2)
			}
		}
		// touch, override
		tmp, err := os.Create(cleanpath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "init:%v", err)
			os.Exit(1)
		}
		defer loggingFileClose("init", tmp)

		// outpath
		flags.data.outputFilePath = cleanpath
	}

	// For specify filetype
	flags.data.filetypes = strings.Split(*flags.suffix, " ")

	// For specify files and dirs
	pathClean := func(str *[]string, in *string) {
		*str = append(*str, strings.Split(*in, *flags.separator)...)
		for i, s := range *str {
			cleanPath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(s)))
			if err != nil {
				log.Printf("init:%v", err)
				continue
			}
			(*str)[i] = cleanPath
		}
	}
	if *flags.fileList != "" {
		pathClean(&flags.data.file, flags.fileList)
	}
	if *flags.dirList != "" {
		pathClean(&flags.data.dir, flags.dirList)
	}
}

// Checking after parsing flags
func argsCheck() {
	if len(flag.Args()) != 0 {
		fmt.Fprintf(os.Stderr, "\ncommand=%v\n\n", os.Args)
		fmt.Fprintf(os.Stderr, "-----| Unknown option |-----\n\n")
		for _, x := range flag.Args() {
			fmt.Fprintln(os.Stderr, x)
		}
		fmt.Fprintln(os.Stderr, "\n-----| Flags |-----")
		flag.PrintDefaults()
		os.Exit(1)
	}
}

func ask(s string) bool {
	fmt.Println(s)
	fmt.Printf("[yes:no]? >>")
	for sc, i := bufio.NewScanner(os.Stdin), 0; sc.Scan() && i < 2; i++ {
		if sc.Err() != nil {
			fmt.Fprintln(os.Stderr, sc.Err())
			os.Exit(3)
		}
		switch sc.Text() {
		case "yes":
			return true
		case "no":
			return false
		default:
			fmt.Println(sc.Text())
			fmt.Printf("[yes:no]? >>")
		}
	}
	return false
}

// Use wait group dirsCrawl
// Recursively search
func dirsCrawl(root string) map[string][]os.FileInfo {
	// mux group
	dirsCache := make(map[string]bool)
	infoCache := make(map[string][]os.FileInfo)
	mux := new(sync.Mutex)

	wg := new(sync.WaitGroup)

	var crawl func(string)
	crawl = func(dirname string) {
		defer wg.Done()
		infos := new([]os.FileInfo)

		// NOTE: Countermove "too many open files"
		mux.Lock()
		ok := func() bool {
			if dirsCache[dirname] {
				return false
			}
			dirsCache[dirname] = true

			var err error
			*infos, err = getInfos(dirname)
			if err != nil {
				log.Printf("crawl:%v", err)
				return false
			}
			infoCache[dirname] = *infos
			return true
		}()
		mux.Unlock()
		if !ok {
			return
		}
		// NOTE: ここまでロックするならスレッドを分ける意味は薄いかも、再考する

		for _, x := range *infos {
			if x.IsDir() {
				wg.Add(1)
				go crawl(filepath.Join(dirname, x.Name()))
			}
		}
	}

	wg.Add(1)
	crawl(root)
	wg.Wait()
	return infoCache
}

// For dirsCrawl and specify filepath
func getInfos(dirname string) ([]os.FileInfo, error) {
	f, err := os.Open(dirname)
	if err != nil {
		return nil, fmt.Errorf("getInfos:%v", err)
	}
	defer loggingFileClose("getInfos", f)

	infos, err := f.Readdir(0)
	if err != nil {
		return nil, fmt.Errorf("getInfos:%v", err)
	}
	return infos, nil
}

func suffixSearcher(filename string, targetSuffix []string) bool {
	for _, x := range targetSuffix {
		if strings.HasSuffix(filename, x) {
			return true
		}
	}
	return false
}

// REMIND: todoListをchannelに変えてstringを投げるようにすれば数を制限したgoroutineが使えそう
func gather(filename string, flags Flags) (todoList []string) {
	f, err := os.Open(filename)
	if err != nil {
		log.Printf("gather:%v", err)
		return nil
	}
	defer loggingFileClose("gather", f)

	sc := bufio.NewScanner(f)
	tmpLineCount := uint(0)
	for i := uint(1); sc.Scan(); i++ {
		if err := sc.Err(); err != nil {
			log.Printf("gather:%v", err)
			return nil
		}
		if *flags.ignoreLong > 0 && len(sc.Text()) > *flags.ignoreLong {
			log.Printf("gather: too long line: %v", filename)
			return nil
		}
		if index := strings.Index(sc.Text(), *flags.keyword); index != -1 {
			if *flags.trim {
				todoList = append(todoList, fmt.Sprintf("L%v:%s", i, sc.Text()[index+len(*flags.keyword):]))
				tmpLineCount = 1
				continue
			} else {
				todoList = append(todoList, fmt.Sprintf("L%v:%s", i, sc.Text()))
				tmpLineCount = 1
				continue
			}
		}
		if tmpLineCount != 0 && tmpLineCount < *flags.lines {
			todoList = append(todoList, fmt.Sprintf(" %v:%s", i, sc.Text()))
			tmpLineCount++
			continue
		} else {
			tmpLineCount = 0
		}
	}
	return todoList
}

// NOTE: gopher増やしまくるとcloseが間に合わなくてosのfile descriptor上限に突っかかる
// goroutine にリミットを付けてファイルオープンを制限して上限に引っかからない様にしてみる
func unlimitedGopherWorks(infoMap map[string][]os.FileInfo, flags Flags) (todoMap map[string][]string) {

	todoMap = make(map[string][]string)

	// NOTE: Countermove "too many open files"!!
	// TODO: 出来れば (descriptor limits / 2) で値を決めたい
	// 環境依存のリミットを取得する良い方法を見つけてない(´・ω・`)
	gophersLimit := *flags.limit // NOTE: This Limit is require (Limit < file descriptor limits)
	var gophersLimiter uint

	mux := new(sync.Mutex)
	wg := new(sync.WaitGroup)

	// Call gather() and append in todoMap
	worker := func(filepath string) {
		defer wg.Done()
		defer func() {
			mux.Lock()
			gophersLimiter--
			mux.Unlock()
		}()

		todoList := gather(filepath, flags)
		if todoList != nil {
			mux.Lock()
			todoMap[filepath] = todoList
			mux.Unlock()
		}
	}

	for dirname, infos := range infoMap {
		for _, info := range infos {
			if suffixSearcher(info.Name(), flags.data.filetypes) {
				wg.Add(1)
				mux.Lock()
				gophersLimiter++
				// NOTE: countermove data race
				tmpLimiter := gophersLimiter
				mux.Unlock()

				go worker(filepath.Join(dirname, info.Name()))

				// NOTE: Countermove "too many open files"
				if tmpLimiter > gophersLimit/2 {
					time.Sleep(time.Microsecond)
				}
				if tmpLimiter > gophersLimit {
					log.Printf("Open files %v over, Do limitation to Gophers!!", gophersLimit)
					log.Printf("Wait gophers...")
					wg.Wait()
					log.Printf("Done!")
					mux.Lock()
					gophersLimiter = 0
					mux.Unlock()
				}
			}
		}
	}
	wg.Wait()
	return todoMap
}

// GophersProc generate TODOMap from file list! gatcha!!
func GophersProc(flags Flags) (todoMap map[string][]string) {
	infoMap := make(map[string][]os.FileInfo)

	// For recursively switch
	if *flags.root != "" {
		if *flags.recursively {
			infoMap = dirsCrawl(*flags.root)
		} else {
			infos, err := getInfos(*flags.root)
			if err != nil {
				log.Printf("GophersProc:%v", err)
			} else {
				infoMap[*flags.root] = infos
			}
		}
	}

	// For specify dirs
	for _, dirname := range flags.data.dir {
		if _, ok := infoMap[dirname]; !ok {
			infos, err := getInfos(dirname)
			if err != nil {
				log.Printf("GophersProc:%v", err)
				continue
			}
			infoMap[dirname] = infos
		}
	}

	// Generate todo list from infoMap
	todoMap = unlimitedGopherWorks(infoMap, flags)

	// For specify files
	for _, s := range flags.data.file {
		if _, ok := todoMap[s]; !ok {
			todoList := gather(s, flags)
			if todoList != nil {
				todoMap[s] = todoList
			}
		}
	}
	return todoMap
}

// OutputTODOList is output crawl results
func OutputTODOList(todoMap map[string][]string, flags Flags) {
	// For Specify output file
	stdout := os.Stdout
	var err error
	if flags.data.outputFilePath != "" {
		stdout, err = os.Create(flags.data.outputFilePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "OutputTODOList:%v", err)
			os.Exit(2)
		}
		defer loggingFileClose("OutputTODOList", stdout)
	}

	// For sort
	if *flags.sort {
		// Optional
		var filenames []string
		for filename := range todoMap {
			filenames = append(filenames, filename)
		}
		sort.Strings(filenames)

		for _, filename := range filenames {
			fmt.Fprintln(stdout, filename)
			for _, todo := range todoMap[filename] {
				fmt.Fprintln(stdout, todo)
			}
			fmt.Fprint(stdout, "\n")
		}
	} else {
		for filename, todoList := range todoMap {
			fmt.Fprintln(stdout, filename)
			for _, s := range todoList {
				fmt.Fprintln(stdout, s)
			}
			fmt.Fprint(stdout, "\n")
		}
	}

	if *flags.result {
		fmt.Fprintln(stdout, "-----| RESULT |-----")
		fmt.Fprintf(stdout, "%v files found have the %q\n\n", len(todoMap), *flags.keyword)
		fmt.Fprintln(stdout, flags)
	}
	if *flags.date {
		fmt.Fprintf(stdout, "\nDATE:%v\n", time.Now())
	}
}

func main() {
	todoMap := GophersProc(flags)
	OutputTODOList(todoMap, flags)
}
