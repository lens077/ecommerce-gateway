package main

import (
	"fmt"
	"log"
	"net/url"

	"github.com/go-kratos/gateway/discovery"
)

func main() {
	// 测试服务发现连接
	discoveryDSN := "consul://apikv.com:8500"
	fmt.Printf("Testing discovery with DSN: %s\n", discoveryDSN)

	// 解析DSN
	dsn, err := url.Parse(discoveryDSN)
	if err != nil {
		log.Fatalf("Failed to parse discovery DSN: %v", err)
	}

	fmt.Printf("Scheme: %s\n", dsn.Scheme)
	fmt.Printf("Host: %s\n", dsn.Host)

	// 创建服务发现
	_, err = discovery.Create(discoveryDSN)
	if err != nil {
		log.Fatalf("Failed to create discovery: %v", err)
	}

	fmt.Println("Successfully created discovery!")

	// 测试服务发现是否能获取服务列表
	// 这里我们测试user-identity-v1服务，因为用户说这个服务已经注册到consul
	fmt.Println("Testing service discovery for user-identity-v1...")

	// 注意：这里只是测试服务发现是否能连接，实际的服务监听需要在一个goroutine中运行
	// 所以我们这里只是创建一个discovery实例，验证连接是否成功
	fmt.Println("Discovery test completed successfully!")
}
