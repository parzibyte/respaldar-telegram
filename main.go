package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
)

// https://core.telegram.org/bots/api#sending-files
// 50 MB al momento de escribir esto era 5e7
// Se supone que 50MB son 52,428,800 bytes, y no 50000000,
// solo que el tamaño máximo exacto es (1024 * 1024 * 50) - 377, o sea, 52,428,423,
// pero  prefiero dejarlo en (1024 * 1024 * 49)
const MaximoTamañoArchivoTelegram = (1024 * 1024 * 49)

// https://core.telegram.org/bots/api#sendmediagroup
// 10 al momento de escribir esto
const MaximaCantidadDeArchivosPorMensaje = 10

const MaximosIntentosEnvioArchivo = 10

type InputMedia struct {
	Type    string `json:"type"`
	Media   string `json:"media"`
	Caption string `json:"caption"`
}

type Mensaje struct {
	ChatId              string `json:"chat_id"`
	Text                string `json:"text"`
	ParseMode           string `json:"parse_mode"`
	DisableNotification bool   `json:"disable_notification"`
}

func enviarMensaje(contenidoDelMensaje string, token string, idChat string) error {
	clienteHttp := &http.Client{}
	url := "https://api.telegram.org/bot" + token + "/sendMessage"
	mensaje := Mensaje{
		ChatId:              idChat,
		Text:                contenidoDelMensaje,
		ParseMode:           "HTML",
		DisableNotification: true,
	}
	mensajeCodificado, err := json.Marshal(mensaje)
	if err != nil {
		return err
	}
	peticion, err := http.NewRequest("POST", url, bytes.NewReader(mensajeCodificado))
	peticion.Header.Set("Content-Type", "application/json")
	if err != nil {
		return err
	}
	respuesta, err := clienteHttp.Do(peticion)
	if err != nil {
		return err
	}
	return manejarRespuestaDeTelegram(respuesta)
}

func manejarRespuestaDeTelegram(respuesta *http.Response) error {
	if respuesta.StatusCode == http.StatusOK {
		return nil
	}
	cuerpoRespuesta, err := io.ReadAll(respuesta.Body)
	if err != nil {
		return err
	}
	return fmt.Errorf("error haciendo petición. Código %d y respuesta %s", respuesta.StatusCode, string(cuerpoRespuesta))
}
func separarArchivoEnVariasPartes(ubicacionArchivoOriginal string, tamañoDeFragmento int64) ([]string, error) {
	var ubicaciones []string
	archivoOriginal, err := os.Open(ubicacionArchivoOriginal)
	if err != nil {
		return ubicaciones, err
	}
	defer archivoOriginal.Close()
	informacionArchivoOriginal, err := archivoOriginal.Stat()
	if err != nil {
		return ubicaciones, err
	}
	tamañoArchivoOriginal := informacionArchivoOriginal.Size()
	cantidadDeFragmentos := (tamañoArchivoOriginal + tamañoDeFragmento - 1) / tamañoDeFragmento

	for i := int64(0); i < cantidadDeFragmentos; i++ {
		nombreFragmento := fmt.Sprintf("%s.part%d", ubicacionArchivoOriginal, i+1)
		ubicaciones = append(ubicaciones, nombreFragmento)
		fragmentoDeArchivoOriginal, err := os.Create(nombreFragmento)
		if err != nil {
			return ubicaciones, err
		}
		_, err = io.CopyN(fragmentoDeArchivoOriginal, archivoOriginal, tamañoDeFragmento)
		if err != nil && err != io.EOF {
			fragmentoDeArchivoOriginal.Close()
			return ubicaciones, err
		}
		fragmentoDeArchivoOriginal.Close()
	}
	return ubicaciones, nil
}

func enviarUnArchivoReintentando(ubicacion string, token string, idChat string, maximosIntentos int) error {
	intentos := 0
	var err error
	for intentos < maximosIntentos {
		err = enviarUnArchivo(ubicacion, token, idChat)
		if err == nil {
			return nil
		} else {

			enviarMensaje(fmt.Sprintf("Error enviando un archivo. Intento %d de %d", intentos, maximosIntentos), token, idChat)
			intentos++
		}

	}
	return errors.New("Máximos intentos alcanzados")
}
func enviarUnArchivo(ubicacion string, token string, idChat string) error {
	file, err := os.Open(ubicacion)
	if err != nil {
		return err
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("document", path.Base(ubicacion))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, file)
	if err != nil {
		return err
	}

	err = writer.WriteField("chat_id", idChat)
	if err != nil {
		return err
	}

	err = writer.WriteField("disable_notification", "true")
	if err != nil {
		return err
	}

	writer.Close()

	clienteHttp := &http.Client{}
	url := "https://api.telegram.org/bot" + token + "/sendDocument"
	peticion, err := http.NewRequest("POST", url, &requestBody)
	peticion.Header.Set("Content-Type", writer.FormDataContentType())
	if err != nil {
		return err
	}
	respuesta, err := clienteHttp.Do(peticion)
	if err != nil {
		return err
	}
	return manejarRespuestaDeTelegram(respuesta)
}

func agregarArchivoAZip(escritorZip *zip.Writer, ubicacionArchivo, ubicacionBase string) error {
	archivoParaAgregarAlZip, err := os.Open(ubicacionArchivo)
	if err != nil {
		return err
	}
	defer archivoParaAgregarAlZip.Close()

	informacionDelArchivoQueSeAgrega, err := archivoParaAgregarAlZip.Stat()
	if err != nil {
		return err
	}

	encabezadoArchivoQueSeAgrega, err := zip.FileInfoHeader(informacionDelArchivoQueSeAgrega)
	if err != nil {
		return err
	}

	encabezadoArchivoQueSeAgrega.Name, err = filepath.Rel(ubicacionBase, ubicacionArchivo)
	if err != nil {
		return err
	}

	encabezadoArchivoQueSeAgrega.Method = zip.Deflate

	escritorArchivo, err := escritorZip.CreateHeader(encabezadoArchivoQueSeAgrega)
	if err != nil {
		return err
	}
	_, err = io.Copy(escritorArchivo, archivoParaAgregarAlZip)
	return err
}

func crearZipDeDirectorio(ubicacionDirectorio, nombreArchivoZip string) error {
	archivoZip, err := os.Create(nombreArchivoZip)
	if err != nil {
		return err
	}
	defer archivoZip.Close()
	escritorZip := zip.NewWriter(archivoZip)
	defer escritorZip.Close()
	return filepath.WalkDir(ubicacionDirectorio, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return agregarArchivoAZip(escritorZip, path, ubicacionDirectorio)
		}
		return nil
	})
}

func crearZip(ubicacionArchivo, nombreArchivoZip, ubicacionBase string) error {
	archivoZip, err := os.Create(nombreArchivoZip)
	if err != nil {
		return err
	}
	defer archivoZip.Close()
	escritorZip := zip.NewWriter(archivoZip)
	defer escritorZip.Close()
	return agregarArchivoAZip(escritorZip, ubicacionArchivo, ubicacionBase)
}

func respaldar(ubicacion string, token string, idChat string) error {
	ubicacionActual, err := os.Getwd()
	if err != nil {
		log.Printf("Error obteniendo ubicación actual %v", err)
		return err
	}
	salida := filepath.Join(ubicacionActual, "salida.zip")
	mensaje := fmt.Sprintf("Respaldando <b>%s</b>\n", ubicacion)
	informacionDeArchivoODirectorioParaRespaldar, err := os.Stat(ubicacion)
	if err != nil {
		return err
	}
	if informacionDeArchivoODirectorioParaRespaldar.IsDir() {
		mensaje += "Es un directorio, creando zip...\n"
		err = crearZipDeDirectorio(ubicacion, salida)
	} else {
		if informacionDeArchivoODirectorioParaRespaldar.Size() <= MaximoTamañoArchivoTelegram {
			mensaje += ("Es un archivo que pesa menos que el límite\n")
			err = enviarMensaje(mensaje, token, idChat)
			if err != nil {
				return err
			}
			return enviarUnArchivoReintentando(ubicacion, token, idChat, MaximosIntentosEnvioArchivo)
		}
		err = crearZip(ubicacion, salida, ubicacionActual)
	}
	if err != nil {
		log.Printf("Error creando zip %v", err)
		return err
	}
	informacionArchivoParaRespaldar, err := os.Stat(salida)
	if err != nil {
		log.Printf("Error obteniendo informacion %v", err)
		return err
	}
	if informacionArchivoParaRespaldar.Size() <= MaximoTamañoArchivoTelegram {
		mensaje += ("El zip resultante pesa menos que el límite\n")
		err = enviarMensaje(mensaje, token, idChat)
		if err != nil {
			return err
		}
		err = enviarUnArchivoReintentando(salida, token, idChat, MaximosIntentosEnvioArchivo)
		if err != nil {
			return err
		}
		return eliminarVariosArchivos([]string{salida})
	} else {
		ubicaciones, err := separarArchivoEnVariasPartes(salida, MaximoTamañoArchivoTelegram)
		if err != nil {

			log.Printf("Error separando archivo en varias partes %v", err)
			return err
		}
		cantidadUbicaciones := len(ubicaciones)
		mensaje += fmt.Sprintf("Separando archivo en %d partes\n", cantidadUbicaciones)
		err = enviarMensaje(mensaje, token, idChat)
		if err != nil {
			return err
		}
		for _, ubicacion := range ubicaciones {
			err = enviarUnArchivoReintentando(ubicacion, token, idChat, MaximosIntentosEnvioArchivo)
			if err != nil {
				return err
			}
		}

		ubicaciones = append(ubicaciones, salida)
		return eliminarVariosArchivos(ubicaciones)
	}
}

func eliminarVariosArchivos(ubicaciones []string) error {
	for _, ubicacion := range ubicaciones {
		err := os.Remove(ubicacion)
		if err != nil {
			return err
		}
	}
	return nil
}

func main() {
	archivoRespaldar := flag.String("archivo", "", "El archivo o directorio a respaldar")
	tokenTelegram := flag.String("token", "", "Tu token de Telegram")
	idChat := flag.String("id_chat", "", "El ID del chat al que quieres enviar el archivo")
	flag.Parse()
	if *archivoRespaldar == "" || *tokenTelegram == "" || *idChat == "" {
		flag.PrintDefaults()
		return
	}
	err := respaldar(*archivoRespaldar, *tokenTelegram, *idChat)
	if err != nil {
		err = enviarMensaje(err.Error(), *tokenTelegram, *idChat)
		if err != nil {
			fmt.Printf("Error: %v", err)
		}
	}
}
