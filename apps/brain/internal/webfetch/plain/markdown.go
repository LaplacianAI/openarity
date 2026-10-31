package plain

import (
	"net/url"
	"strings"

	"codeberg.org/readeck/go-readability/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
)

func toMarkdown(doc string, page *url.URL) (title, md string, err error) {
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(), table.NewTablePlugin(),
	))
	conv.Register.TagType("svg", converter.TagTypeRemove, converter.PriorityStandard)
	domain := converter.WithDomain(page.String())

	art, err := readability.FromReader(strings.NewReader(doc), page)
	if err == nil {
		title = art.Title()
		if art.Node != nil {
			b, err := conv.ConvertNode(art.Node, domain)
			if err != nil {
				return "", "", err
			}
			if md = strings.TrimSpace(string(b)); md != "" {
				return title, md, nil
			}
		}
	}

	md, err = conv.ConvertString(doc, domain)
	return title, strings.TrimSpace(md), err
}
