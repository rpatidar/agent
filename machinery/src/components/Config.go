package components

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io/ioutil"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/InVisionApp/conjungo"
	"github.com/kerberos-io/agent/machinery/src/database"
	"github.com/kerberos-io/agent/machinery/src/log"
	"github.com/kerberos-io/agent/machinery/src/models"
	"gopkg.in/mgo.v2/bson"
)

func GetImageFromFilePath() (image.Image, error) {
	snapshotDirectory := "./data/snapshots"
	files, err := ioutil.ReadDir(snapshotDirectory)
	if err == nil && len(files) > 1 {
		sort.Slice(files, func(i, j int) bool {
			return files[i].ModTime().Before(files[j].ModTime())
		})
		filePath := "./data/snapshots/" + files[1].Name()
		f, err := os.Open(filePath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		image, _, err := image.Decode(f)
		return image, err
	}
	return nil, errors.New("Could not find a snapshot in " + snapshotDirectory)
}

func GetSnapshot() string {
	var snapshot string
	files, err := ioutil.ReadDir("./data/snapshots")
	if err == nil && len(files) > 1 {
		sort.Slice(files, func(i, j int) bool {
			return files[i].ModTime().Before(files[j].ModTime())
		})
		f, _ := os.Open("./data/snapshots/" + files[1].Name())
		defer f.Close()
		// Read entire JPG into byte slice.
		reader := bufio.NewReader(f)
		content, _ := ioutil.ReadAll(reader)
		// Encode as base64.
		snapshot = base64.StdEncoding.EncodeToString(content)
	}
	return snapshot
}

// ReadUserConfig Reads the user configuration of the Kerberos Open Source instance.
// This will return a models.User struct including the username, password,
// selected language, and if the installation was completed or not.
func ReadUserConfig() (userConfig models.User) {
	for {
		jsonFile, err := os.Open("./data/config/user.json")
		if err != nil {
			fmt.Println(err)
			fmt.Println("Config file is not found " + "./data/config/user.json" + ", trying again in 5s.")
			time.Sleep(5 * time.Second)
		} else {
			fmt.Println("Successfully Opened user.json")
			byteValue, _ := ioutil.ReadAll(jsonFile)
			err = json.Unmarshal(byteValue, &userConfig)
			if err != nil {
				fmt.Println("JSON file not valid: " + err.Error())
			} else {
				jsonFile.Close()
				break
			}
			time.Sleep(5 * time.Second)
		}
		jsonFile.Close()
	}

	return
}

func OpenConfig(configuration *models.Configuration) {

	// We are checking which deployment this is running, so we can load
	// into the configuration as expected.

	if os.Getenv("DEPLOYMENT") == "factory" || os.Getenv("MACHINERY_ENVIRONMENT") == "kubernetes" {

		// Factory deployment means that configuration is stored in MongoDB
		// Multiple agents have there configuration stored, and can benefit from
		// the concept of a global concept.

		session := database.New().Copy()
		defer session.Close()
		db := session.DB(database.DatabaseName)
		collection := db.C("configuration")

		collection.Find(bson.M{
			"type": "global",
		}).One(&configuration.GlobalConfig)

		collection.Find(bson.M{
			"type": "config",
			"name": os.Getenv("DEPLOYMENT_NAME"),
		}).One(&configuration.CustomConfig)

		// We will merge both configs in a single config file.
		// Read again from database but this store overwrite the same object.

		opts := conjungo.NewOptions()
		opts.SetTypeMergeFunc(
			reflect.TypeOf(""),
			func(t, s reflect.Value, o *conjungo.Options) (reflect.Value, error) {
				targetStr, _ := t.Interface().(string)
				sourceStr, _ := s.Interface().(string)
				finalStr := targetStr
				if sourceStr != "" {
					finalStr = sourceStr
				}
				return reflect.ValueOf(finalStr), nil
			},
		)

		// Merge Config toplevel
		conjungo.Merge(&configuration.Config, configuration.GlobalConfig, opts)
		conjungo.Merge(&configuration.Config, configuration.CustomConfig, opts)

		// Merge Kerberos Vault settings
		var kerberosvault models.KStorage
		conjungo.Merge(&kerberosvault, configuration.GlobalConfig.KStorage, opts)
		conjungo.Merge(&kerberosvault, configuration.CustomConfig.KStorage, opts)
		configuration.Config.KStorage = &kerberosvault

		// Merge Kerberos S3 settings
		var s3 models.S3
		conjungo.Merge(&s3, configuration.GlobalConfig.S3, opts)
		conjungo.Merge(&s3, configuration.CustomConfig.S3, opts)
		configuration.Config.S3 = &s3

	} else if os.Getenv("DEPLOYMENT") == "" || os.Getenv("DEPLOYMENT") == "agent" {

		// Local deployment means we do a stand-alone installation
		// Configuration is stored into a json file, and there is only 1 agent.

		// Open device config
		for {
			jsonFile, err := os.Open("./data/config/config.json")
			if err != nil {
				log.Log.Error("Config file is not found " + "./data/config/config.json" + ", trying again in 5s.")
				time.Sleep(5 * time.Second)
			} else {
				log.Log.Info("Successfully Opened config.json from " + configuration.Name)
				byteValue, _ := ioutil.ReadAll(jsonFile)
				err = json.Unmarshal(byteValue, &configuration.Config)
				jsonFile.Close()
				if err != nil {
					fmt.Println("JSON file not valid: " + err.Error())
				} else {
					err = json.Unmarshal(byteValue, &configuration.CustomConfig)
					if err != nil {
						fmt.Println("JSON file not valid: " + err.Error())
					} else {
						break
					}
				}
				time.Sleep(5 * time.Second)
			}
			jsonFile.Close()
		}

	}

	return
}

// This function will override the configuration with environment variables.
func OverrideWithEnvironmentVariables(configuration *models.Configuration) {
	environmentVariables := os.Environ()
	for _, env := range environmentVariables {
		if strings.Contains(env, "AGENT_") {
			key := strings.Split(env, "=")[0]
			value := os.Getenv(key)
			switch key {

			/* General configuration */
			case "AGENT_KEY":
				configuration.Config.Key = value
				break
			case "AGENT_NAME":
				configuration.Config.Name = value
				break
			case "AGENT_TIMEZONE":
				configuration.Config.Timezone = value
				break
			case "AGENT_OFFLINE":
				configuration.Config.Offline = value
				break
			case "AGENT_AUTO_CLEAN":
				configuration.Config.AutoClean = value
				break
			case "AGENT_AUTO_CLEAN_MAX_SIZE":
				size, err := strconv.ParseInt(value, 10, 64)
				if err == nil {
					configuration.Config.MaxDirectorySize = size
				}
				break

			/* Camera configuration */
			case "AGENT_CAPTURE_IPCAMERA_RTSP":
				configuration.Config.Capture.IPCamera.RTSP = value
				break
			case "AGENT_CAPTURE_IPCAMERA_SUB_RTSP":
				configuration.Config.Capture.IPCamera.SubRTSP = value
				break

				/* ONVIF connnection settings */
			case "AGENT_CAPTURE_IPCAMERA_ONVIF":
				isEnabled := value == " true"
				configuration.Config.Capture.IPCamera.ONVIF = isEnabled
				break
			case "AGENT_CAPTURE_IPCAMERA_ONVIF_XADDR":
				configuration.Config.Capture.IPCamera.ONVIFXAddr = value
				break
			case "AGENT_CAPTURE_IPCAMERA_ONVIF_USERNAME":
				configuration.Config.Capture.IPCamera.ONVIFUsername = value
				break
			case "AGENT_CAPTURE_IPCAMERA_ONVIF_PASSWORD":
				configuration.Config.Capture.IPCamera.ONVIFPassword = value
				break

			/* Recording mode */
			case "AGENT_CAPTURE_CONTINUOUS":
				configuration.Config.Capture.Continuous = value
				break
			case "AGENT_CAPTURE_PRERECORDING":
				duration, err := strconv.ParseInt(value, 10, 64)
				if err == nil {
					configuration.Config.Capture.PreRecording = duration
				}
				break
			case "AGENT_CAPTURE_POSTRECORDING":
				duration, err := strconv.ParseInt(value, 10, 64)
				if err == nil {
					configuration.Config.Capture.PostRecording = duration
				}
				break
			case "AGENT_CAPTURE_MAXLENGTH":
				duration, err := strconv.ParseInt(value, 10, 64)
				if err == nil {
					configuration.Config.Capture.MaxLengthRecording = duration
				}
				break
			case "AGENT_CAPTURE_PIXEL_CHANGE":
				count, err := strconv.Atoi(value)
				if err == nil {
					configuration.Config.Capture.PixelChangeThreshold = count
				}
				break
			case "AGENT_CAPTURE_FRAGMENTED":
				configuration.Config.Capture.Fragmented = value
				break
			case "AGENT_CAPTURE_FRAGMENTED_DURATION":
				duration, err := strconv.ParseInt(value, 10, 64)
				if err == nil {
					configuration.Config.Capture.FragmentedDuration = duration
				}
				break

			/* Cloud settings for persisting recordings */
			case "AGENT_CLOUD":
				configuration.Config.Cloud = value
				break

			/* When connected and storing in Kerberos Hub (SAAS) */
			case "AGENT_HUB_URI":
				configuration.Config.HubURI = value
				break
			case "AGENT_HUB_KEY":
				configuration.Config.HubKey = value
				break
			case "AGENT_HUB_PRIVATE_KEY":
				configuration.Config.HubPrivateKey = value
				break
			case "AGENT_HUB_USERNAME":
				configuration.Config.S3.Username = value
				break
			case "AGENT_HUB_SITE":
				configuration.Config.HubSite = value
				break

			/* MQTT settings for bi-directional communication */
			case "AGENT_MQTT_URI":
				configuration.Config.MQTTURI = value
				break
			case "AGENT_MQTT_USERNAME":
				configuration.Config.MQTTUsername = value
				break
			case "AGENT_MQTT_PASSWORD":
				configuration.Config.MQTTPassword = value
				break

			/* WebRTC settings for live-streaming (remote) */
			case "AGENT_STUN_URI":
				configuration.Config.STUNURI = value
				break
			case "AGENT_TURN_URI":
				configuration.Config.TURNURI = value
				break
			case "AGENT_TURN_USERNAME":
				configuration.Config.TURNUsername = value
				break
			case "AGENT_TURN_PASSWORD":
				configuration.Config.TURNPassword = value
				break

			/* When storing in a Kerberos Vault */
			case "AGENT_KERBEROSVAULT_URI":
				configuration.Config.KStorage.URI = value
				break
			case "AGENT_KERBEROSVAULT_ACCESS_KEY":
				configuration.Config.KStorage.AccessKey = value
				break
			case "AGENT_KERBEROSVAULT_SECRET_KEY":
				configuration.Config.KStorage.SecretAccessKey = value
				break
			case "AGENT_KERBEROSVAULT_PROVIDER":
				configuration.Config.KStorage.Provider = value
				break
			case "AGENT_KERBEROSVAULT_DIRECTORY":
				configuration.Config.KStorage.Directory = value
				break

			}
		}
	}
}

func SaveConfig(config models.Config, configuration *models.Configuration, communication *models.Communication) error {
	if !communication.IsConfiguring.IsSet() {
		communication.IsConfiguring.Set()

		err := StoreConfig(config)
		if err != nil {
			communication.IsConfiguring.UnSet()
			return err
		}

		select {
		case communication.HandleBootstrap <- "restart":
		default:
		}

		communication.IsConfiguring.UnSet()

		return nil
	} else {
		return errors.New("☄ Already reconfiguring")
	}
}

func StoreConfig(config models.Config) error {
	// Save into database
	if os.Getenv("DEPLOYMENT") == "factory" || os.Getenv("MACHINERY_ENVIRONMENT") == "kubernetes" {
		// Write to mongodb
		session := database.New().Copy()
		defer session.Close()
		db := session.DB(database.DatabaseName)
		collection := db.C("configuration")

		err := collection.Update(bson.M{
			"type": "config",
			"name": os.Getenv("DEPLOYMENT_NAME"),
		}, &config)
		return err

		// Save into file
	} else if os.Getenv("DEPLOYMENT") == "" || os.Getenv("DEPLOYMENT") == "agent" {
		res, _ := json.MarshalIndent(config, "", "\t")
		err := ioutil.WriteFile("./data/config/config.json", res, 0644)
		return err
	}

	return errors.New("Not able to update config")
}
