// Command server 启动小倾角初稳性核算 HTTP 服务。
//
// 环境变量：
//
//	PORT      监听端口，默认 8080
//	DATA_FILE 装载状态档持久化文件，默认 /data/conditions.json
//	GIN_MODE  gin 运行模式（release/debug/test）
package main

import (
	"log"
	"os"

	"shipstability/api"
	"shipstability/internal/archive"
	"shipstability/internal/store"
)

func main() {
	dataFile := envOr("DATA_FILE", "/data/conditions.json")
	port := envOr("PORT", "8080")

	fs, err := store.NewFileStore(dataFile)
	if err != nil {
		log.Fatalf("初始化持久化存储失败: %v", err)
	}
	conds := archive.NewService(fs)
	if err := conds.SeedDefaults(); err != nil {
		log.Fatalf("预置装载状态算例失败: %v", err)
	}

	srv := api.NewServer(conds)
	log.Printf("小倾角初稳性核算服务启动，监听 :%s，档案文件 %s", port, dataFile)
	if err := srv.Router().Run(":" + port); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
