package controller

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/asaskevich/govalidator"
	"github.com/labstack/echo/v4"
	"github.com/mxschmitt/playwright-go"
)

type Http struct {
	Browser        *playwright.Browser
	MaxActivePages uint64
	activePages    uint64
}

// Ping godoc
// @Summary Check if the server is ready to accept requests
// @Description Check if the server is ready to accept requests
// @Tags ping
// @Success 200 {string} string	"ok"
// @failure 400 {string} string	"error"
// @Router /ping [get]
func (ctrl *Http) Ping(c echo.Context) error {
	if ctrl.Browser.IsConnected() {
		return c.HTML(http.StatusOK, "")
	}
	return c.HTML(http.StatusServiceUnavailable, "")
}

type ConvertRequest struct {
	Filename   string `form:"filename" query:"filename"`
	URL        string `form:"url" query:"url" valid:"url"`
	Locale     string `form:"locale" query:"locale"`
	Javascript *bool  `form:"javascript" query:"javascript"`
	Format     string `form:"format" query:"format" valid:"in(Letter|Legal|Tabloid|Ledger|A0|A1|A2|A4|A5|A6)"`
	Offline    bool   `form:"offline" query:"offline"`
	Media      string `form:"media" query:"media" valid:"in(screen|print)"`

	MarginTop    string `form:"marginTop" query:"marginTop"`
	MarginRight  string `form:"marginRight" query:"marginRight"`
	MarginBottom string `form:"marginBottom" query:"marginBottom"`
	MarginLeft   string `form:"marginLeft" query:"marginLeft"`

	FooterTemplate string `form:"footerTemplate"`
	HeaderTemplate string `form:"headerTemplate"`
	HTML           string `form:"html"`
}

// ConvertHTML godoc
// @Summary Converts a URL or HTML to PDF document
// @Description Converts a URL or HTML to PDF document
// @Tags convert
// @Accept multipart/form-data
// @Param url formData string false "URL"
// @Param filename formData string false "Filename of the resulting pdf" default(result.pdf)
// @Param html formData string false "HTML to convert"
// @Param locale formData string false "Page locale" default(en-US)
// @Param javascript formData bool false "Enable Javascript" default(true)
// @Param format formData string false "Page format" default(A4)
// @Param offline formData bool false "Disable network connectivity" default(false)
// @Param media formData string false "Page media mode" default(print) Enums(print,screen)
// @Param marginTop formData string false "Page margin top"
// @Param marginRight formData string false "Page margin right"
// @Param marginBottom formData string false "Page margin bottom"
// @Param footerTemplate formData string false "Page footer template"
// @Param headerTemplate formData string false "Page header template"
// @Produce application/pdf
// @Success 200 {file} file
// @Router /convert [post]
func (ctrl *Http) ConvertHTML(c echo.Context) error {
	if ctrl.MaxActivePages > 0 && ctrl.activePages > ctrl.MaxActivePages {
		c.Logger().Errorf("too many requests. Max actives pages of %d has been reached. Please try again later.", ctrl.MaxActivePages)
		return c.HTML(http.StatusTooManyRequests, "")
	}

	/*
		Extract data from request
	*/
	u := new(ConvertRequest)
	if err := c.Bind(u); err != nil {
		c.Logger().Errorf("request bind: %s", err)
		return c.HTML(http.StatusBadRequest, "")
	}

	/*
		Request validation
	*/
	_, err := govalidator.ValidateStruct(u)
	if err != nil {
		c.Logger().Errorf("request validation: %s", err)
		return c.String(http.StatusBadRequest, err.Error())
	}

	/*
		Footer template
	*/
	footerTemplateFile, err := c.FormFile("footerTemplate")
	if err == nil {
		footerTemplateSrc, err := footerTemplateFile.Open()
		if err != nil {
			c.Logger().Errorf("could not open footerTemplate: %s", err)
			return c.String(http.StatusBadRequest, "")
		}
		defer footerTemplateSrc.Close()

		if b, err := ioutil.ReadAll(footerTemplateSrc); err == nil {
			u.FooterTemplate = string(b)
		} else {
			c.Logger().Errorf("could not read footerTemplate: %s", err)
		}
	} else if err != http.ErrMissingFile {
		c.Logger().Errorf("could not get form value footerTemplate: %s", err)
		return c.String(http.StatusBadRequest, "")
	}

	/*
		Header template
	*/
	headerTemplateFile, err := c.FormFile("headerTemplate")
	if err == nil {
		headerTemplateSrc, err := headerTemplateFile.Open()
		if err != nil {
			c.Logger().Errorf("could not open headerTemplate: %s", err)
			return c.String(http.StatusBadRequest, "")
		}
		defer headerTemplateSrc.Close()

		if b, err := ioutil.ReadAll(headerTemplateSrc); err == nil {
			u.HeaderTemplate = string(b)
		} else {
			c.Logger().Errorf("could not read headerTemplate: %s", err)
		}
	} else if err != http.ErrMissingFile {
		c.Logger().Errorf("could not get form value headerTemplate: %s", err)
		return c.String(http.StatusBadRequest, "")
	}

	/*
		HTML template
	*/
	htmlTemplateFile, err := c.FormFile("html")
	if err == nil {
		htmlTemplateSrc, err := htmlTemplateFile.Open()
		if err != nil {
			c.Logger().Errorf("could not open html: %s", err)
			return c.String(http.StatusBadRequest, "")
		}
		defer htmlTemplateSrc.Close()

		if b, err := ioutil.ReadAll(htmlTemplateSrc); err == nil {
			u.HTML = string(b)
		} else {
			c.Logger().Errorf("could not read html: %s", err)
		}
	} else if err != http.ErrMissingFile {
		c.Logger().Errorf("could not get form value html: %s", err)
		return c.String(http.StatusBadRequest, "")
	}

	/*
		Defaults
	*/
	if u.Filename == "" {
		u.Filename = "result.pdf"
	}
	if u.FooterTemplate == "" {
		u.FooterTemplate = "<span></span>"
	}
	if u.HeaderTemplate == "" {
		u.HeaderTemplate = "<span></span>"
	}
	if u.Format == "" {
		u.Format = "A4"
	}
	if u.Media == "" {
		u.Media = "print"
	}
	if u.Javascript == nil {
		u.Javascript = playwright.Bool(true)
	}

	/*
		Create new browser context to avoid side-effects (cookies, storage etc...)
	*/
	browserContextOptions := playwright.BrowserNewContextOptions{
		JavaScriptEnabled: u.Javascript,
		Locale:            playwright.String(u.Locale),
	}
	browserContext, err := ctrl.Browser.NewContext(browserContextOptions)
	if err != nil {
		c.Logger().Errorf("could not create new context: %s", err)
		return c.HTML(http.StatusInternalServerError, "")
	}

	/*
		Open a new page. Playwright will handle the queue.
	*/
	page, err := browserContext.NewPage(playwright.BrowserNewPageOptions{
		Offline: playwright.Bool(u.Offline),
	})
	if err != nil {
		c.Logger().Error("could not create new page")
		return c.HTML(http.StatusInternalServerError, "")
	}

	atomic.AddUint64(&ctrl.activePages, 1)
	defer func() {
		atomic.AddUint64(&ctrl.activePages, ^uint64(0))
	}()

	page.SetDefaultTimeout(10000)

	if err := page.EmulateMedia(playwright.PageEmulateMediaOptions{Media: u.Media}); err != nil {
		c.Logger().Errorf("could not emulate media: %s", err)
		return c.HTML(http.StatusBadGateway, "")
	}

	if u.URL != "" {
		_, err = page.Goto(u.URL)
		if err != nil {
			c.Logger().Errorf("could not go to page: %s", err)
			return c.HTML(http.StatusBadGateway, "")
		}
	} else {
		err := page.SetContent(u.HTML)
		if err != nil {
			c.Logger().Errorf("could not set page content: %s", err)
			return c.HTML(http.StatusInternalServerError, "")
		}

	}

	/*
		Render page
	*/
	pdfBytes, err := page.PDF(playwright.PagePdfOptions{
		DisplayHeaderFooter: playwright.Bool(true),
		PrintBackground:     playwright.Bool(true),
		FooterTemplate:      playwright.String(u.FooterTemplate),
		HeaderTemplate:      playwright.String(u.HeaderTemplate),
		Format:              playwright.String(u.Format),
		Margin: &playwright.PagePdfMargin{
			Top:    u.MarginTop,
			Right:  u.MarginRight,
			Bottom: u.MarginBottom,
			Left:   u.MarginLeft,
		},
	})
	if err != nil {
		c.Logger().Errorf("could not create pdf from page: %s", err)
		return c.HTML(http.StatusInternalServerError, "")
	}

	if err := browserContext.Close(); err != nil {
		c.Logger().Errorf("could not close browser context: %s", err)
	}

	tmpfile, err := ioutil.TempFile(os.TempDir(), "fay-conversion-")
	if err != nil {
		c.Logger().Errorf("could not create temp pdf file: %s", err)
		return c.HTML(http.StatusInternalServerError, "")
	}

	if _, err = io.Copy(tmpfile, bytes.NewReader(pdfBytes)); err != nil {
		c.Logger().Errorf("could not write pdf to disk: %s", err)
		return c.HTML(http.StatusInternalServerError, "")
	}

	if err := c.Attachment(tmpfile.Name(), u.Filename); err != nil {
		c.Logger().Errorf("could not attach pdf: %s", err)
	}

	_ = tmpfile.Close()
	_ = os.Remove(tmpfile.Name())

	return nil
}
