package engine

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/asdine/storm"
	Settings "github.com/deranjer/goTorrent/settings"
	"github.com/deranjer/goTorrent/storage"
	Storage "github.com/deranjer/goTorrent/storage"
	"github.com/sirupsen/logrus"
)

func secondsToMinutes(inSeconds int64) string {
	minutes := inSeconds / 60
	seconds := inSeconds % 60
	minutesString := fmt.Sprintf("%d", minutes)
	secondsString := fmt.Sprintf("%d", seconds)
	str := minutesString + " Min/ " + secondsString + " Sec"
	return str
}

//VerifyData just verifies the data of a torrent by hash
func VerifyData(singleTorrent *torrent.Torrent) {
	singleTorrent.VerifyData()
}

//MakeRange creates a range of pieces to set their priority based on a file
func MakeRange(min, max int) []int {
	a := make([]int, max-min+1)
	for i := range a {
		a[i] = min + i
	}
	return a
}

//HumanizeBytes returns a nice humanized version of bytes in either GB or MB
func HumanizeBytes(bytes float32) string {
	if bytes < 1000000 { //if we have less than 1MB in bytes convert to KB
		pBytes := fmt.Sprintf("%.2f", bytes/1024)
		pBytes = pBytes + " KB"
		return pBytes
	}
	bytes = bytes / 1024 / 1024 //Converting bytes to a useful measure
	if bytes > 1024 {
		pBytes := fmt.Sprintf("%.2f", bytes/1024)
		pBytes = pBytes + " GB"
		return pBytes
	}
	pBytes := fmt.Sprintf("%.2f", bytes) //If not too big or too small leave it as MB
	pBytes = pBytes + " MB"
	return pBytes
}

//CopyFile takes a source file string and a destination file string and copies the file
func CopyFile(srcFile string, destFile string) { //TODO move this to our imported copy repo
	fileContents, err := os.Open(srcFile)
	defer fileContents.Close()
	if err != nil {
		Logger.WithFields(logrus.Fields{"File": srcFile, "Error": err}).Error("Cannot open source file")
	}
	outfileContents, err := os.Create(destFile)
	defer outfileContents.Close()
	if err != nil {
		Logger.WithFields(logrus.Fields{"File": destFile, "Error": err}).Error("Cannot open destination file")
	}
	_, err = io.Copy(outfileContents, fileContents)
	if err != nil {
		Logger.WithFields(logrus.Fields{"Source File": srcFile, "Destination File": destFile, "Error": err}).Error("Cannot write contents to destination file")
	}

}

//SetFilePriority sets the priorities for all of the files in all of the torrents
func SetFilePriority(t *torrent.Client, db *storm.DB) {
	storedTorrents := Storage.FetchAllStoredTorrents(db)
	for _, singleTorrent := range t.Torrents() {
		for _, storedTorrent := range storedTorrents {
			if storedTorrent.Hash == singleTorrent.InfoHash().String() {
				for _, file := range singleTorrent.Files() {
					for _, storedFile := range storedTorrent.TorrentFilePriority {
						if storedFile.TorrentFilePath == file.DisplayPath() {
							switch storedFile.TorrentFilePriority {
							case "High":
								file.SetPriority(torrent.PiecePriorityHigh)
							case "Normal":
								file.SetPriority(torrent.PiecePriorityNormal)
							case "Cancel":
								file.SetPriority(torrent.PiecePriorityNone)
							default:
								file.SetPriority(torrent.PiecePriorityNormal)
							}
						}
					}
				}
			}
		}
	}
}

//CalculateTorrentSpeed is used to calculate the torrent upload and download speed over time c is current clientdb, oc is last client db to calculate speed over time
func CalculateTorrentSpeed(t *torrent.Torrent, c *ClientDB, oc ClientDB, completedSize int64) {
	now := time.Now()
	bytes := completedSize
	bytesUpload := t.Stats().BytesWrittenData
	dt := float32(now.Sub(oc.UpdatedAt))     // get the delta time length between now and last updated
	db := float32(bytes - oc.BytesCompleted) //getting the delta bytes
	rate := db * (float32(time.Second) / dt) // converting into seconds
	dbU := float32(bytesUpload.Int64() - oc.DataBytesWritten)
	rateUpload := dbU * (float32(time.Second) / dt)
	if rate >= 0 {
		rateMB := rate / 1024 / 1024 //creating MB to calculate ETA
		c.DownloadSpeed = fmt.Sprintf("%.2f", rateMB)
		c.DownloadSpeed = c.DownloadSpeed + " MB/s"
		c.downloadSpeedInt = int64(rate)
	}
	if rateUpload >= 0 {
		rateUpload = rateUpload / 1024 / 1024
		c.UploadSpeed = fmt.Sprintf("%.2f", rateUpload)
		c.UploadSpeed = c.UploadSpeed + " MB/s"

	}
	c.UpdatedAt = now
}

//CalculateDownloadSize will calculate the download size once file priorities are sorted out
func CalculateDownloadSize(tFromStorage *Storage.TorrentLocal, activeTorrent *torrent.Torrent) int64 {
	var totalLength int64
	for _, file := range tFromStorage.TorrentFilePriority {
		if file.TorrentFilePriority != "Cancel" {
			totalLength = totalLength + file.TorrentFileSize
		}
	}
	return totalLength
}

//CalculateCompletedSize will be used to calculate how much of the actual torrent we have completed minus the canceled files (even if they have been partially downloaded)
func CalculateCompletedSize(tFromStorage *Storage.TorrentLocal, activeTorrent *torrent.Torrent) int64 {
	var discardByteLength int64
	for _, storageFile := range tFromStorage.TorrentFilePriority {
		if storageFile.TorrentFilePriority == "Cancel" { //If the file is canceled don't count it as downloaded
			for _, activeFile := range activeTorrent.Files() {
				if activeFile.DisplayPath() == storageFile.TorrentFilePath { //match the file from storage to active
					for _, piece := range activeFile.State() {
						if piece.Partial || piece.Complete {
							discardByteLength = discardByteLength + piece.Bytes
						}
					}
				}
			}
		}
	}
	downloadedLength := activeTorrent.BytesCompleted() - discardByteLength
	if downloadedLength < 0 {
		downloadedLength = 0
	}
	return downloadedLength
}

//CalculateTorrentETA is used to estimate the remaining dl time of the torrent based on the speed that the MB are being downloaded
func CalculateTorrentETA(tSize int64, tBytesCompleted int64, c *ClientDB) {
	missingBytes := tSize - tBytesCompleted
	if missingBytes == 0 {
		c.ETA = "Done"
	} else if c.downloadSpeedInt == 0 {
		c.ETA = "N/A"
	} else {
		ETASeconds := missingBytes / c.downloadSpeedInt
		str := secondsToMinutes(ETASeconds) //converting seconds to minutes + seconds
		c.ETA = str
	}
}

//CalculateUploadRatio calculates the download to upload ratio so you can see if you are being a good seeder
func CalculateUploadRatio(t *torrent.Torrent, c *ClientDB) string {
	if c.TotalUploadedBytes > 0 && t.BytesCompleted() > 0 { //If we have actually started uploading and downloading stuff start calculating our ratio
		uploadRatio := fmt.Sprintf("%.2f", float64(c.TotalUploadedBytes)/float64(t.BytesCompleted()))
		return uploadRatio
	}
	uploadRatio := "0.00" //we haven't uploaded anything so no upload ratio just pass a string directly
	return uploadRatio
}

//StopTorrent stops the torrent, updates the database and sends a message.  Since stoptorrent is called by each loop (individually) no need to call an array
func StopTorrent(singleTorrent *torrent.Torrent, torrentLocalStorage *Storage.TorrentLocal, db *storm.DB) {
	if torrentLocalStorage.TorrentStatus == "Stopped" { //if we are already stopped
		Logger.WithFields(logrus.Fields{"Torrent Name": torrentLocalStorage.TorrentName}).Info("Torrent Already Stopped, returning...")
		return
	}
	torrentLocalStorage.TorrentStatus = "Stopped"
	torrentLocalStorage.MaxConnections = 0
	singleTorrent.SetMaxEstablishedConns(0)
	DeleteTorrentFromQueues(singleTorrent.InfoHash().String(), db)
	Storage.UpdateStorageTick(db, *torrentLocalStorage)
	CreateServerPushMessage(ServerPushMessage{MessageType: "serverPushMessage", MessageLevel: "success", Payload: "Torrent Stopped!"}, Conn)
	Logger.WithFields(logrus.Fields{"Torrent Name": torrentLocalStorage.TorrentName}).Info("Torrent Stopped Success!")
}

//AddTorrentToForceStart forces torrent to be high priority on start
func AddTorrentToForceStart(torrentLocalStorage *Storage.TorrentLocal, singleTorrent *torrent.Torrent, db *storm.DB) {
	torrentQueues := Storage.FetchQueues(db)
	for index, torrentHash := range torrentQueues.ActiveTorrents {
		if torrentHash == singleTorrent.InfoHash().String() { //If torrent already in active remove from active
			torrentQueues.ActiveTorrents = append(torrentQueues.ActiveTorrents[:index], torrentQueues.ActiveTorrents[index+1:]...)
		}
	}
	for index, queuedTorrentHash := range torrentQueues.QueuedTorrents { //Removing from the queued torrents if in queued torrents
		if queuedTorrentHash == singleTorrent.InfoHash().String() {
			torrentQueues.QueuedTorrents = append(torrentQueues.QueuedTorrents[:index], torrentQueues.QueuedTorrents[index+1:]...)
		}
	}
	singleTorrent.NewReader()
	singleTorrent.SetMaxEstablishedConns(80)
	torrentQueues.ActiveTorrents = append(torrentQueues.ActiveTorrents, singleTorrent.InfoHash().String())
	torrentLocalStorage.TorrentStatus = "ForceStart"
	torrentLocalStorage.MaxConnections = 80
	for _, file := range singleTorrent.Files() {
		for _, sentFile := range torrentLocalStorage.TorrentFilePriority {
			if file.DisplayPath() == sentFile.TorrentFilePath {
				switch sentFile.TorrentFilePriority {
				case "High":
					file.SetPriority(torrent.PiecePriorityHigh)
				case "Normal":
					file.SetPriority(torrent.PiecePriorityNormal)
				case "Cancel":
					file.SetPriority(torrent.PiecePriorityNone)
				default:
					file.SetPriority(torrent.PiecePriorityNormal)
				}
			}
		}
	}
	Logger.WithFields(logrus.Fields{"Torrent Name": torrentLocalStorage.TorrentName}).Info("Adding Torrent to ForceStart Queue")
	Storage.UpdateStorageTick(db, *torrentLocalStorage)
	Storage.UpdateQueues(db, torrentQueues)
}

//AddTorrentToActive adds a torrent to the active slice
func AddTorrentToActive(torrentLocalStorage *Storage.TorrentLocal, singleTorrent *torrent.Torrent, db *storm.DB) {
	torrentQueues := Storage.FetchQueues(db)
	if torrentLocalStorage.TorrentStatus == "Stopped" {
		Logger.WithFields(logrus.Fields{"Torrent Name": torrentLocalStorage.TorrentName}).Info("Torrent set as stopped, skipping add")
		return
	}
	for _, torrentHash := range torrentQueues.ActiveTorrents {
		if torrentHash == singleTorrent.InfoHash().String() { //If torrent already in active skip
			return
		}
	}
	for index, queuedTorrentHash := range torrentQueues.QueuedTorrents { //Removing from the queued torrents if in queued torrents
		if queuedTorrentHash == singleTorrent.InfoHash().String() {
			torrentQueues.QueuedTorrents = append(torrentQueues.QueuedTorrents[:index], torrentQueues.QueuedTorrents[index+1:]...)
		}
	}
	singleTorrent.NewReader()
	singleTorrent.SetMaxEstablishedConns(80)
	torrentQueues.ActiveTorrents = append(torrentQueues.ActiveTorrents, singleTorrent.InfoHash().String())
	torrentLocalStorage.TorrentStatus = "Running"
	torrentLocalStorage.MaxConnections = 80
	for _, file := range singleTorrent.Files() {
		for _, sentFile := range torrentLocalStorage.TorrentFilePriority {
			if file.DisplayPath() == sentFile.TorrentFilePath {
				switch sentFile.TorrentFilePriority {
				case "High":
					file.SetPriority(torrent.PiecePriorityHigh)
				case "Normal":
					file.SetPriority(torrent.PiecePriorityNormal)
				case "Cancel":
					file.SetPriority(torrent.PiecePriorityNone)
				default:
					file.SetPriority(torrent.PiecePriorityNormal)
				}
			}
		}
	}
	Logger.WithFields(logrus.Fields{"Torrent Name": torrentLocalStorage.TorrentName}).Info("Adding Torrent to Active Queue (Manual Call)")
	Storage.UpdateStorageTick(db, *torrentLocalStorage)
	Storage.UpdateQueues(db, torrentQueues)
}

//RemoveTorrentFromActive forces a torrent to be removed from the active list if the max limit is already there and user forces a new torrent to be added
func RemoveTorrentFromActive(torrentLocalStorage *Storage.TorrentLocal, singleTorrent *torrent.Torrent, db *storm.DB) {
	torrentQueues := Storage.FetchQueues(db)
	for x, torrentHash := range torrentQueues.ActiveTorrents {
		if torrentHash == singleTorrent.InfoHash().String() {
			torrentQueues.ActiveTorrents = append(torrentQueues.ActiveTorrents[:x], torrentQueues.ActiveTorrents[x+1:]...)
			torrentQueues.QueuedTorrents = append(torrentQueues.QueuedTorrents, torrentHash)
			torrentLocalStorage.TorrentStatus = "Queued"
			torrentLocalStorage.MaxConnections = 0
			singleTorrent.SetMaxEstablishedConns(0)
			Storage.UpdateQueues(db, torrentQueues)
			//AddTorrentToQueue(torrentLocalStorage, singleTorrent, db) //Adding the lasttorrent from active to queued
			Storage.UpdateStorageTick(db, *torrentLocalStorage)
		}
	}

}

//DeleteTorrentFromQueues deletes the torrent from all queues (for a stop or delete action)
func DeleteTorrentFromQueues(torrentHash string, db *storm.DB) {
	torrentQueues := Storage.FetchQueues(db)
	for x, torrentHashActive := range torrentQueues.ActiveTorrents { //FOR EXTRA CAUTION deleting it from both queues in case a mistake occurred.
		if torrentHash == torrentHashActive {
			torrentQueues.ActiveTorrents = append(torrentQueues.ActiveTorrents[:x], torrentQueues.ActiveTorrents[x+1:]...)
			Logger.Info("Removing Torrent from Active:  ", torrentHash)
		}
	}
	for x, torrentHashQueued := range torrentQueues.QueuedTorrents { //FOR EXTRA CAUTION deleting it from both queues in case a mistake occurred.
		if torrentHash == torrentHashQueued {
			torrentQueues.QueuedTorrents = append(torrentQueues.QueuedTorrents[:x], torrentQueues.QueuedTorrents[x+1:]...)
			Logger.Info("Removing Torrent from Queued", torrentHash)
		}
	}
	for x, torrentHashActive := range torrentQueues.ForcedTorrents { //FOR EXTRA CAUTION deleting it from all queues in case a mistake occurred.
		if torrentHash == torrentHashActive {
			torrentQueues.ForcedTorrents = append(torrentQueues.ForcedTorrents[:x], torrentQueues.ForcedTorrents[x+1:]...)
			Logger.Info("Removing Torrent from Forced:  ", torrentHash)
		}
	}
	Storage.UpdateQueues(db, torrentQueues)
	Logger.WithFields(logrus.Fields{"Torrent Hash": torrentHash, "TorrentQueues": torrentQueues}).Info("Removing Torrent from all Queues")
}

//AddTorrentToQueue adds a torrent to the queue
func AddTorrentToQueue(torrentLocalStorage *Storage.TorrentLocal, singleTorrent *torrent.Torrent, db *storm.DB) {
	torrentQueues := Storage.FetchQueues(db)
	for _, torrentHash := range torrentQueues.QueuedTorrents {
		if singleTorrent.InfoHash().String() == torrentHash { //don't add duplicate to que but do everything else (TODO, maybe find a better way?)
			singleTorrent.SetMaxEstablishedConns(0)
			torrentLocalStorage.MaxConnections = 0
			torrentLocalStorage.TorrentStatus = "Queued"
			Logger.WithFields(logrus.Fields{"TorrentName": torrentLocalStorage.TorrentName}).Info("Adding torrent to the queue, not active")
			Storage.UpdateStorageTick(db, *torrentLocalStorage)
			return
		}
	}
	torrentQueues.QueuedTorrents = append(torrentQueues.QueuedTorrents, singleTorrent.InfoHash().String())
	singleTorrent.SetMaxEstablishedConns(0)
	torrentLocalStorage.MaxConnections = 0
	torrentLocalStorage.TorrentStatus = "Queued"
	Logger.WithFields(logrus.Fields{"TorrentName": torrentLocalStorage.TorrentName}).Info("Adding torrent to the queue, not active")
	Storage.UpdateQueues(db, torrentQueues)
	Storage.UpdateStorageTick(db, *torrentLocalStorage)
}

//RemoveDuplicatesFromQueues removes any duplicates from torrentQueues.QueuedTorrents (which will happen if it is read in from DB)
func RemoveDuplicatesFromQueues(db *storm.DB) {
	torrentQueues := Storage.FetchQueues(db)
	for _, torrentHash := range torrentQueues.ActiveTorrents {
		for i, queuedHash := range torrentQueues.QueuedTorrents {
			if torrentHash == queuedHash {
				torrentQueues.QueuedTorrents = append(torrentQueues.QueuedTorrents[:i], torrentQueues.QueuedTorrents[i+1:]...)
			}
		}
	}
	Storage.UpdateQueues(db, torrentQueues)
}

//ValidateQueues is a sanity check that runs every tick to make sure the queues are in order... tried to avoid this but seems to be required
func ValidateQueues(db *storm.DB, config Settings.FullClientSettings, tclient *torrent.Client) {
	torrentQueues := Storage.FetchQueues(db)
	for len(torrentQueues.ActiveTorrents) > config.MaxActiveTorrents {
		removeTorrent := torrentQueues.ActiveTorrents[:1]
		for _, singleTorrent := range tclient.Torrents() {
			if singleTorrent.InfoHash().String() == removeTorrent[0] {
				singleTorrentFromStorage := Storage.FetchTorrentFromStorage(db, removeTorrent[0])
				RemoveTorrentFromActive(&singleTorrentFromStorage, singleTorrent, db)
			}
		}
	}
	torrentQueues = Storage.FetchQueues(db)
	for _, singleTorrent := range tclient.Torrents() {
		singleTorrentFromStorage := Storage.FetchTorrentFromStorage(db, singleTorrent.InfoHash().String())
		if singleTorrentFromStorage.TorrentStatus == "Stopped" {
			continue
		}
		for _, queuedTorrent := range torrentQueues.QueuedTorrents { //If we have a queued torrent that is missing data, and an active torrent that is seeding, then prioritize the missing data one
			if singleTorrent.InfoHash().String() == queuedTorrent {
				if singleTorrent.BytesMissing() > 0 {
					for _, activeTorrent := range torrentQueues.ActiveTorrents {
						for _, singleActiveTorrent := range tclient.Torrents() {
							if activeTorrent == singleActiveTorrent.InfoHash().String() {
								if singleActiveTorrent.Seeding() == true {
									singleActiveTFS := Storage.FetchTorrentFromStorage(db, activeTorrent)
									Logger.WithFields(logrus.Fields{"TorrentName": singleActiveTFS.TorrentName}).Info("Seeding, Removing from active to add queued")
									RemoveTorrentFromActive(&singleActiveTFS, singleActiveTorrent, db)
									singleQueuedTFS := Storage.FetchTorrentFromStorage(db, queuedTorrent)
									Logger.WithFields(logrus.Fields{"TorrentName": singleQueuedTFS.TorrentName}).Info("Adding torrent to the queue, not active")
									AddTorrentToActive(&singleQueuedTFS, singleTorrent, db)
								}
							}
						}
					}
				}
			}
		}
	}
}

//CalculateTorrentStatus is used to determine what the STATUS column of the frontend will display ll2
func CalculateTorrentStatus(t *torrent.Torrent, c *ClientDB, config Settings.FullClientSettings, tFromStorage *storage.TorrentLocal, bytesCompleted int64, totalSize int64, torrentQueues Storage.TorrentQueues, db *storm.DB) {
	if tFromStorage.TorrentStatus == "Stopped" {
		c.Status = "Stopped"
		return
	}
	//Only has 2 states in storage, stopped or running, so we know it should be running, and the websocket request handled updating the database with connections and status
	for _, torrentHash := range torrentQueues.QueuedTorrents {
		if tFromStorage.Hash == torrentHash {
			c.Status = "Queued"
			return
		}
	}
	bytesMissing := totalSize - bytesCompleted
	c.MaxConnections = 80
	t.SetMaxEstablishedConns(80)
	if t.Seeding() && t.Stats().ActivePeers > 0 && bytesMissing == 0 {
		c.Status = "Seeding"
	} else if t.Stats().ActivePeers > 0 && bytesMissing > 0 {
		c.Status = "Downloading"
	} else if t.Stats().ActivePeers == 0 && bytesMissing == 0 {
		c.Status = "Completed"
	} else if t.Stats().ActivePeers == 0 && bytesMissing > 0 {
		c.Status = "Awaiting Peers"
	} else {
		c.Status = "Unknown"
	}
}
