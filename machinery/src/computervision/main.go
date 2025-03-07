package computervision

import (
	"image"
	"io/ioutil"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/kerberos-io/agent/machinery/src/capture"
	"github.com/kerberos-io/agent/machinery/src/log"
	"github.com/kerberos-io/agent/machinery/src/models"
	"github.com/kerberos-io/joy4/av/pubsub"

	geo "github.com/kellydunn/golang-geo"
	"github.com/kerberos-io/joy4/av"
	"github.com/kerberos-io/joy4/cgo/ffmpeg"
	"gocv.io/x/gocv"
)

func GetRGBImage(pkt av.Packet, dec *ffmpeg.VideoDecoder, decoderMutex *sync.Mutex) gocv.Mat {
	var rgb gocv.Mat
	img, err := capture.DecodeImage(pkt, dec, decoderMutex)
	if err == nil && img != nil {
		rgb, _ = ToRGB8(img.Image)
		gocv.Resize(rgb, &rgb, image.Pt(rgb.Cols()/4, rgb.Rows()/4), 0, 0, gocv.InterpolationArea)
	}
	return rgb
}

func GetImage(pkt av.Packet, dec *ffmpeg.VideoDecoder, decoderMutex *sync.Mutex) gocv.Mat {
	var rgb gocv.Mat
	img, err := capture.DecodeImage(pkt, dec, decoderMutex)

	if err == nil && img != nil {

		// Check if we need to scale down.
		width := img.Width()
		height := img.Height()
		newWidth := width
		newHeight := height

		// Try minify twice.
		scaleFactor := 1.0
		if newWidth > 800 {
			newWidth = width / 2
			newHeight = height / 2
			scaleFactor *= 2
		}
		if newWidth > 800 {
			newWidth = width / 2
			newHeight = height / 2
			scaleFactor *= 2
		}
		if newWidth > 800 {
			newWidth = width / 2
			newHeight = height / 2
			scaleFactor *= 2
		}

		im := img.Image
		bounds := im.Bounds()
		x := bounds.Dx()
		y := bounds.Dy()
		if x > 0 && y > 0 {
			rgb, _ = ToRGB8(im)
			img.Free()
			if scaleFactor > 1 {
				gocv.Resize(rgb, &rgb, image.Pt(newWidth, newHeight), 0, 0, gocv.InterpolationArea)
			}
		}
	}
	return rgb
}

func ToGray(rgb gocv.Mat) (gocv.Mat, error) {
	gray := gocv.NewMat()
	gocv.CvtColor(rgb, &gray, gocv.ColorBGRToGray)
	rgb.Close()
	return gray, nil
}

func ToRGB8(img image.YCbCr) (gocv.Mat, error) {
	bounds := img.Bounds()
	x := bounds.Dx()
	y := bounds.Dy()
	bytes := make([]byte, 0, x*y*3)
	for j := bounds.Min.Y; j < bounds.Max.Y; j++ {
		for i := bounds.Min.X; i < bounds.Max.X; i++ {
			iy := img.At(i, j)
			if iy != nil {
				r, g, b, _ := iy.RGBA()
				bytes = append(bytes, byte(b>>8), byte(g>>8), byte(r>>8))
			}
		}
	}
	return gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC3, bytes)
}

func ProcessMotion(motionCursor *pubsub.QueueCursor, configuration *models.Configuration, communication *models.Communication, mqttClient mqtt.Client, decoder *ffmpeg.VideoDecoder, decoderMutex *sync.Mutex) { //, wg *sync.WaitGroup) {
	log.Log.Debug("ProcessMotion: started")
	config := configuration.Config

	var isPixelChangeThresholdReached = false
	var changesToReturn = 0

	if config.Capture.Continuous == "true" {

		log.Log.Info("ProcessMotion: Continuous recording, so no motion detection.")

	} else {

		log.Log.Info("ProcessMotion: Motion detection enabled.")

		key := config.HubKey

		// Initialise first 2 elements
		var matArray [3]*gocv.Mat
		j := 0

		//for pkt := range packets {
		var cursorError error
		var pkt av.Packet

		for cursorError == nil {
			pkt, cursorError = motionCursor.ReadPacket()
			// Check If valid package.
			if len(pkt.Data) > 0 && pkt.IsKeyFrame {
				rgb := GetImage(pkt, decoder, decoderMutex)
				gray, _ := ToGray(rgb)
				matArray[j] = &gray
				j++
			}
			if j == 2 {
				break
			}
		}

		img := matArray[0]
		if img != nil {

			// Calculate mask
			var polyObjects []geo.Polygon
			for _, polygon := range config.Region.Polygon {
				coords := polygon.Coordinates
				poly := geo.Polygon{}
				for _, c := range coords {
					x := c.X
					y := c.Y
					p := geo.NewPoint(x, y)
					if !poly.Contains(p) {
						poly.Add(p)
					}
				}
				polyObjects = append(polyObjects, poly)
			}

			rows := img.Rows()
			cols := img.Cols()
			var coordinatesToCheck [][]int
			for y := 0; y < rows; y++ {
				for x := 0; x < cols; x++ {
					for _, poly := range polyObjects {
						point := geo.NewPoint(float64(x), float64(y))
						if poly.Contains(point) {
							coordinatesToCheck = append(coordinatesToCheck, []int{x, y})
							break
						}
					}
				}
			}

			// Start the motion detection
			i := 0
			loc, _ := time.LoadLocation(config.Timezone)

			for cursorError == nil {
				pkt, cursorError = motionCursor.ReadPacket()

				// Check If valid package.
				if len(pkt.Data) == 0 || !pkt.IsKeyFrame {
					continue
				}

				rgb := GetImage(pkt, decoder, decoderMutex)
				gray, _ := ToGray(rgb)
				matArray[2] = &gray

				// Store snapshots (jpg) or hull.
				files, err := ioutil.ReadDir("./data/snapshots")
				if err == nil {
					sort.Slice(files, func(i, j int) bool {
						return files[i].ModTime().Before(files[j].ModTime())
					})
					if len(files) > 3 {
						os.Remove("./data/snapshots/" + files[0].Name())
					}
				}
				t := strconv.FormatInt(time.Now().Unix(), 10)
				snapshotRGB := GetImage(pkt, decoder, decoderMutex)
				gocv.IMWrite("./data/snapshots/"+t+".png", snapshotRGB)
				snapshotRGB.Close()

				// Check if continuous recording.
				if config.Capture.Continuous == "true" {

					// Do not do anything! Just sleep as there is no
					// motion detection needed

				} else { // Do motion detection.

					// Check if within time interval
					detectMotion := true
					now := time.Now().In(loc)
					weekday := now.Weekday()
					hour := now.Hour()
					minute := now.Minute()
					second := now.Second()
					timeInterval := config.Timetable[int(weekday)]
					if timeInterval != nil {
						start1 := timeInterval.Start1
						end1 := timeInterval.End1
						start2 := timeInterval.Start2
						end2 := timeInterval.End2
						currentTimeInSeconds := hour*60*60 + minute*60 + second
						if (currentTimeInSeconds >= start1 && currentTimeInSeconds <= end1) ||
							(currentTimeInSeconds >= start2 && currentTimeInSeconds <= end2) {

						} else {
							detectMotion = false
							log.Log.Debug("ProcessMotion: Time interval not valid, disabling motion detection.")
						}
					}

					// Remember additional information about the result of findmotion
					isPixelChangeThresholdReached, changesToReturn = FindMotion(matArray, coordinatesToCheck, config.Capture.PixelChangeThreshold)

					if detectMotion && isPixelChangeThresholdReached {

						if mqttClient != nil {
							mqttClient.Publish("kerberos/"+key+"/device/"+config.Key+"/motion", 2, false, "motion")
						}

						//FIXME: In the future MotionDataPartial should be replaced with MotionDataFull
						dataToPass := models.MotionDataPartial{
							Timestamp:       time.Now().Unix(),
							NumberOfChanges: changesToReturn,
						}
						communication.HandleMotion <- dataToPass //Save data to the channel
					}
				}

				matArray[0].Close()
				matArray[0] = matArray[1]
				matArray[1] = matArray[2]
				i++
				runtime.GC()
				debug.FreeOSMemory()
			}
		}
		if img != nil {
			img.Close()
		}
		runtime.GC()
		debug.FreeOSMemory()
	}

	log.Log.Debug("ProcessMotion: finished")
}

func FindMotion(matArray [3]*gocv.Mat, coordinatesToCheck [][]int, pixelChangeThreshold int) (thresholdReached bool, changesDetected int) {

	h1 := gocv.NewMat()
	gocv.AbsDiff(*matArray[2], *matArray[0], &h1)
	h2 := gocv.NewMat()
	gocv.AbsDiff(*matArray[2], *matArray[1], &h2)

	and := gocv.NewMat()
	gocv.BitwiseAnd(h1, h2, &and)
	h1.Close()
	h2.Close()

	thresh := gocv.NewMat()
	gocv.Threshold(and, &thresh, 30.0, 255.0, gocv.ThresholdBinary)
	and.Close()

	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 3))
	eroded := gocv.NewMat()
	gocv.Erode(thresh, &eroded, kernel)
	thresh.Close()
	kernel.Close()

	changes := 0
	for _, c := range coordinatesToCheck {
		value := eroded.GetUCharAt(c[1], c[0])
		if value > 0 {
			changes++
		}
	}
	eroded.Close()

	log.Log.Info("FindMotion: Number of changes detected:" + strconv.Itoa(changes))

	if pixelChangeThreshold == 0 {
		pixelChangeThreshold = 75 // Keep hardcoded value of 75 for now if no value is given for changes treshold in config.json
	}

	changesDetected = changes                              // Assign final amount of changes to the return variable
	return changes > pixelChangeThreshold, changesDetected // Return bool ifReachedThreshold AND the amount of changes detected in total
}
