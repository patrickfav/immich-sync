package main

type sourceData struct {
	Albums        map[string][]TakeoutFile
	CreationTimes map[string]int64
	Descriptions  map[string]string
}

type localTakeoutSource struct{}

func (localTakeoutSource) ReadAlbums() (sourceData, error) {
	if err := readFiles(false); err != nil {
		return sourceData{}, err
	}
	return sourceData{
		Albums:        takeout,
		CreationTimes: takeoutAlbumCreationTimes,
		Descriptions:  takeoutAlbumDescriptions,
	}, nil
}

func (localTakeoutSource) ReadFiles() ([]TakeoutFile, error) {
	if err := readFiles(true); err != nil {
		return nil, err
	}
	return append([]TakeoutFile(nil), takeoutFiles...), nil
}
