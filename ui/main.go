package main

import (
	functions "hollishake/godeploy-function"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func main() {
	state := functions.NewState()
	a := app.New()
	window := a.NewWindow("GoDeploy")

	details := container.NewMax(NewEmptyProjectDetail())
	projectsView := NewProjectsView(window, &state, func(project functions.Project) {
		details.Objects = []fyne.CanvasObject{
			NewProjectDetail(project),
		}
		details.Refresh()
	})
	content := container.NewBorder(
		widget.NewLabel("Workspace"),
		nil,
		nil,
		nil,
		container.NewPadded(details),
	)
	split := container.NewHSplit(projectsView, content)
	split.Offset = 0.35
	window.SetContent(split)
	window.Resize(fyne.NewSize(800, 500))
	window.ShowAndRun()
}
