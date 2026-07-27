package main

import (
	"embed"
	"log"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// assets 将 Vite 的生产构建产物直接嵌入桌面可执行文件。
// “all:” 会连同以点开头的静态文件一起打包，运行时不再依赖外部前端目录。
//
//go:embed all:frontend/dist
var assets embed.FS

// main 负责装配 Wails 桌面壳、主窗口和系统托盘。
// 业务状态并不保存在 GUI 进程中；前端通过 DesktopService 获取独立 daemon 的连接信息。
func main() {
	// DesktopService 是唯一暴露给 React 的原生桥接服务，负责发现或拉起 daemon。
	desktopService := &DesktopService{}
	app := application.New(application.Options{
		Name:        "CTF-BTFly",
		Description: "Local-first autonomous CTF solving workbench",
		Services: []application.Service{
			application.NewService(desktopService),
		},
		Assets:  application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Mac:     application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})

	// 主窗口采用固定的最小尺寸，以保证三栏工作台在缩放后仍然可用。
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "CTF-BTFly", Width: 1440, Height: 900, MinWidth: 1120, MinHeight: 720,
		// Wails v3 默认阻止来自资源管理器的外部文件投放。开启后，
		// 带 data-file-drop-target 的 React 区域才能收到 WebView2 拖放事件。
		EnableFileDrop: true,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(8, 12, 17),
		URL:              "/",
	})

	// Windows/Linux 上点击关闭按钮只隐藏窗口，后台 CTF 任务继续运行；
	// 真正退出必须经过托盘动作中的运行任务检查。
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		window.Hide()
		event.Cancel()
	})

	// requestQuit 先请求 daemon 自检。若仍有运行、创建中或暂停的任务，
	// daemon 会返回冲突状态及任务清单，桌面端据此阻止误退出。
	requestQuit := func() {
		check, err := desktopService.PrepareExit()
		if err != nil {
			window.Show()
			app.Dialog.Warning().SetTitle("无法安全退出").SetMessage("无法确认后台任务状态。请检查本地 daemon 后重试。\n\n" + err.Error()).AttachToWindow(window).Show()
			return
		}
		if !check.CanExit {
			titles := make([]string, 0, len(check.Running))
			for _, task := range check.Running {
				titles = append(titles, task.Title+"（"+task.Status+"）")
			}
			window.Show()
			app.Dialog.Warning().SetTitle("仍有项目正在运行").SetMessage("请先在工作台中手动中止以下项目，再从系统托盘退出：\n\n" + strings.Join(titles, "\n")).AttachToWindow(window).Show()
			return
		}
		app.Quit()
	}

	// 托盘是隐藏窗口后的重新入口，也提供唯一的安全退出入口。
	systemTray := app.SystemTray.New()
	systemTray.SetTooltip("CTF-BTFly · CTF 自主解题工作台")
	menu := app.NewMenu()
	menu.Add("显示工作台").OnClick(func(_ *application.Context) { window.Show() })
	menu.Add("退出程序").OnClick(func(_ *application.Context) { requestQuit() })
	systemTray.SetMenu(menu)
	systemTray.OnClick(func() { window.Show() })

	// Run 会阻塞到应用退出；启动或事件循环错误属于不可恢复错误。
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
