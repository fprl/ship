package helper

import "github.com/fprl/ship/internal/podmanruntime"

type imageRelease = podmanruntime.Image
type imageEntry = podmanruntime.ImageEntry

func podmanAllImagesForEnv(app, env string) ([]imageRelease, error) {
	return podmanruntime.CLI().Images(app, env)
}

func podmanImages(app, env string) ([]imageRelease, error) {
	return podmanAllImagesForEnv(app, env)
}

func imageReleasesFromEntries(app, env string, entries []imageEntry) []imageRelease {
	return podmanruntime.ImagesFromEntries(app, env, entries)
}
