// Command xiaomu-probe loads a config URL and runs home → category →
// detail → play for the first N sites. The URL is a runtime argument and
// is not stored by this program.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/probe"
)

func main() {
	configURL := flag.String("config", "", "配置地址（必填，只在本次运行使用）")
	line := flag.String("line", "", "仓库线路名称，缺省取第一条")
	query := flag.String("query", "", "可选搜索词；用于搜索型站点，替代首分类探测")
	n := flag.Int("n", 3, "只探测前 N 个站点")
	timeout := flag.Duration("timeout", 30*time.Second, "每个站点的总超时")
	assets := flag.String("assets", "", "覆盖 assets:// 脚本的目录（跑上游 drpy2 引擎时用）")
	flag.Parse()
	if *configURL == "" {
		fmt.Fprintln(os.Stderr, "需要 -config。小幕不内置任何配置地址。")
		flag.Usage()
		os.Exit(2)
	}
	rows, err := probe.Run(context.Background(), probe.Options{
		ConfigURL: *configURL,
		Line:      *line,
		Query:     *query,
		N:         *n,
		Timeout:   *timeout,
		AssetsDir: *assets,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	probe.Fprint(os.Stdout, rows)
	for _, r := range rows {
		if r.Status == "FAIL" {
			os.Exit(1)
		}
	}
}
