package main

import (
	"embed"
	"log"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
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
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "CTF-BTFly", Width: 1440, Height: 900, MinWidth: 1120, MinHeight: 720,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(8, 12, 17),
		URL:              "/",
	})
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		window.Hide()
		event.Cancel()
	})

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

	systemTray := app.SystemTray.New()
	systemTray.SetTooltip("CTF-BTFly · CTF 自主解题工作台")
	menu := app.NewMenu()
	menu.Add("显示工作台").OnClick(func(_ *application.Context) { window.Show() })
	menu.Add("退出程序").OnClick(func(_ *application.Context) { requestQuit() })
	systemTray.SetMenu(menu)
	systemTray.OnClick(func() { window.Show() })
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
