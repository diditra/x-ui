package service

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"x-ui/database/model"
	"x-ui/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

var TgSessions map[int64]*TgSession = make(map[int64]*TgSession)

type TgSession struct {
	state           stateFn
	canAcceptPhoto  bool
	telegramService TelegramService
	client          *model.TgClient
	clientRequest   *model.TgClientMsg
	lang            string
}

type (
	commandEntity struct {
		key  string
		desc string
	}
)

const (
	StartCmdKey          = string("start")
	MainMenuKey          = string("menu")
	UsageCmdKey          = string("usage")
	RegisterCmdKey       = string("register")
	ReferToFriendsCmdKey = string("refer")
	ContactSupportCmdKey = string("support")

	// Default language is Persian/Farsi, change to "en" for English
	defaultLang = string("fa")
)

type stateFn func(*TgSession, *tgbotapi.Message) *tgbotapi.MessageConfig

func CreateChatMenu() []tgbotapi.BotCommand {
	commands := []commandEntity{
		{
			key:  MainMenuKey,
			desc: "Main Menu",
		},
	}

	tgCommands := make([]tgbotapi.BotCommand, 0, 1)
	for _, cmd := range commands {
		tgCommands = append(tgCommands, tgbotapi.BotCommand{
			Command:     "/" + string(cmd.key),
			Description: cmd.desc,
		})
	}
	return tgCommands
}

//***************************************************************************
// States
//***************************************************************************

func InitFSM() *TgSession {
	return &TgSession{
		state:          IdleState,
		canAcceptPhoto: false,
	}
}

func IdleState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {
	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	cmd := ""
	args := ""

	if msg.IsCommand() {
		cmd = msg.Command()
		args = msg.CommandArguments()
	} else {
		// Handle commands coming from callbacks
		if strings.HasPrefix(msg.Text, "/") {
			cmd = strings.TrimPrefix(msg.Text, "/")
		} else {
			resp.Text = Tr("msgChooseFromMenu", s.lang)
			s.appendChatMenu(&resp)
			return &resp
		}
	}

	crmEnabled := s.telegramService.settingService.GetTgCrmEnabled()

	s.loadClientLang(msg.Chat.ID)

	switch cmd {
	case StartCmdKey:
		resp = tgbotapi.NewMessage(msg.Chat.ID, "")
		getLanguagesSelector(&resp)
		s.state = ChooseLangState

	case MainMenuKey:
		resp.Text = Tr("msgChooseFromMenu", s.lang)
		s.appendChatMenu(&resp)

	case UsageCmdKey:
		client, err := s.telegramService.getTgClient(msg.Chat.ID)
		if args == "" {
			if err != nil {
				resp.Text = Tr("msgNotRegisteredEnterLink", s.lang)
				s.state = RegUuidState
			} else {
				if client.Enabled {
					s.telegramService.GetAllClientUsages(msg.Chat.ID)
					return nil
				} else {
					resp.Text = Tr("msgAlreadyRegistered", s.lang)
				}
			}
		} else {
			uuid := parseUuid(args)
			if uuid == "" {
				resp = tgbotapi.NewMessage(msg.Chat.ID, Tr("incorrectUuid", s.lang))
				logger.Error("No uuid found in the argument: ", args)
				return &resp
			}

			response, err := s.telegramService.GetClientUsage(msg.Chat.ID, uuid, s.telegramService.settingService.GetTgCrmEnabled(), s.lang)
			if err != nil {
				return response
			}
			resp = *response

			if client == nil {
				name := msg.Chat.FirstName + " " + msg.Chat.LastName + " @" + msg.Chat.UserName
				s.client = &model.TgClient{
					Enabled:  true,
					ChatID:   msg.Chat.ID,
					Name:     name,
					Uid:      args,
					Language: s.lang,
				}
				err = s.telegramService.AddTgClient(s.client)
			} else {
				if s.telegramService.CheckIfClientExists(args) {
					client.Uid = args
					err = s.telegramService.UpdateClient(client)
				}
			}
			if err != nil {
				resp.Text = Tr("msgInternalError", s.lang)
				resp.ReplyMarkup = nil
			}
		}

	case RegisterCmdKey:
		if !crmEnabled {
			resp.Text = Tr("msgIncorrectCmd", s.lang)
			break
		}

		if s.telegramService.settingService.GetTgCrmSoldOut() {
			resp.Text = Tr("msgSoldOut", s.lang)
			break
		}

		client, _ := s.telegramService.getTgClient(msg.Chat.ID)
		s.client = client

		s.showAccListKeyboard(&resp)

		s.clientRequest = &model.TgClientMsg{
			ChatID: msg.Chat.ID,
			Type:   model.Registration,
		}

		s.state = RegAccTypeState

	case ReferToFriendsCmdKey:
		if !crmEnabled {
			resp.Text = Tr("msgIncorrectCmd", s.lang)
			break
		}

		client, _ := s.telegramService.getTgClient(msg.Chat.ID)
		s.client = client

		if client == nil {
			resp.Text = Tr("msgNotRegistered", s.lang)
			break
		}
		referToFriendsMsg, err := s.telegramService.settingService.GetTgReferToFriendsMsg()
		if err != nil {
			resp.Text = Tr("msgInternalError", s.lang)
		}
		referToFriendsMsg = s.telegramService.replaceMarkup(&referToFriendsMsg, client.ChatID, "")
		resp.Text = referToFriendsMsg
		resp.ParseMode = tgbotapi.ModeHTML

	case ContactSupportCmdKey:
		contactSupportMsg, err := s.telegramService.settingService.GetTgContactSupportMsg()
		if err != nil {
			resp.Text = Tr("msgInternalError", s.lang)
		}
		resp.Text = contactSupportMsg
		resp.ParseMode = tgbotapi.ModeHTML

	default:
		resp.Text = Tr("msgIncorrectCmd", s.lang)

	}
	return &resp
}

func ChooseLangState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {

	if isTgCommand(msg) {
		return abort(s, msg)
	}

	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	if !strings.HasPrefix(msg.Text, "lang:") {
		getLanguagesSelector(&resp)
	}
	s.lang = strings.TrimPrefix(msg.Text, "lang:")
	resp.Text = Tr("msgChooseFromMenu", s.lang)
	s.telegramService.SaveClientLanguage(msg.Chat.ID, s.lang)

	s.appendChatMenu(&resp)
	s.state = IdleState
	return &resp
}

func RegAccTypeState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {

	if isTgCommand(msg) {
		return abort(s, msg)
	}

	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	orderType := strings.TrimSpace(msg.Text)
	if orderType == "" {
		resp.Text = Tr("msgIncorrectPackageNo", s.lang)
		s.state = IdleState
		return &resp
	}

	s.clientRequest.Msg += "Type: " + orderType

	if s.client == nil {
		name := msg.Chat.FirstName + " " + msg.Chat.LastName + " @" + msg.Chat.UserName
		s.client = &model.TgClient{
			Enabled:  false,
			ChatID:   msg.Chat.ID,
			Name:     name,
			Language: s.lang,
		}
		s.state = RegEmailState
		resp.Text = Tr("msgEnterEmail", s.lang)
	} else {
		moneyTransferInstructions, err := s.telegramService.settingService.GetTgMoneyTransferMsg()
		if err != nil {
			logger.Error("RegAccTypeState failed to get money transfer instructions: ", err)
			resp.Text = Tr("msgInternalError", s.lang)
			s.state = IdleState
			return &resp
		}
		s.canAcceptPhoto = true // allow the client to send receipts
		resp.Text = moneyTransferInstructions
		s.state = SendReceiptState
	}

	return &resp
}

func RegEmailState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {

	if isTgCommand(msg) {
		return abort(s, msg)
	}

	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	email := strings.TrimSpace(msg.Text)
	if _, err := mail.ParseAddress(email); err != nil {
		resp.Text = Tr("msgIncorrectEmail", s.lang)
		return &resp
	}

	s.client.Email = email
	resp.Text = Tr("msgAddNotes", s.lang)

	s.state = RegNoteState
	return &resp
}

func RegNoteState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {

	if isTgCommand(msg) {
		return abort(s, msg)
	}

	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	note := strings.TrimSpace(msg.Text)

	s.clientRequest.Msg += ", Note: " + note
	err := s.telegramService.AddTgClient(s.client)

	if err != nil {
		logger.Error(err)
		resp.Text = Tr("msgInternalError", s.lang)
	} else {
		moneyTransferInstructions, err := s.telegramService.settingService.GetTgMoneyTransferMsg()
		if err != nil {
			logger.Error("RegNoteState failed to get money transfer instructions: ", err)
			resp.Text = Tr("msgInternalError", s.lang)
			s.state = IdleState
			return &resp
		}
		s.canAcceptPhoto = true // allow the client to send receipts
		resp.Text = moneyTransferInstructions
		s.state = SendReceiptState
	}

	return &resp
}

func RegUuidState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {

	if isTgCommand(msg) {
		return abort(s, msg)
	}

	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	uuid := parseUuid(msg.Text)

	if uuid == "" || !s.telegramService.CheckIfClientExists(uuid) {
		resp.Text = Tr("msgIncorrectUuid", s.lang)
		return &resp
	}

	name := msg.Chat.FirstName + " " + msg.Chat.LastName + " @" + msg.Chat.UserName
	s.client = &model.TgClient{
		Enabled:  true,
		ChatID:   msg.Chat.ID,
		Name:     name,
		Uid:      uuid,
		Language: s.lang,
	}

	err := s.telegramService.AddTgClient(s.client)
	if err != nil {
		logger.Error(err)
		resp.Text = Tr("msgInternalError", s.lang)
	} else {
		resp.Text = Tr("msgRegisterSuccess", s.lang)
	}

	s.state = IdleState
	return &resp
}

func SendReceiptState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {
	if isTgCommand(msg) {
		return abort(s, msg)
	}

	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	if s.clientRequest == nil {
		resp.Text = Tr("msgInternalError", s.lang)
		s.canAcceptPhoto = false
		s.state = IdleState
		return &resp
	}

	if msg.Photo == nil {
		resp.Text = Tr("msgIncorrectReceipt", s.lang)
		return &resp
	}

	// Put the order up on the panel
	err := s.telegramService.PushTgClientMsg(s.clientRequest)
	if err != nil {
		logger.Error(err)
		resp.Text = Tr("msgInternalError", s.lang)
	}

	err = s.telegramService.SendMsgToAdmin("New client request! Please visit the panel.")
	if err != nil {
		logger.Error("SendReceiptState failed to send msg to admin:", err)
	}

	s.canAcceptPhoto = false
	s.state = IdleState
	resp.Text = Tr("msgOrderRegistered", s.lang)

	return &resp
}

func ConfirmResetState(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {
	resp := tgbotapi.NewMessage(msg.Chat.ID, "")
	if strings.ToLower(msg.Text) == "yes" {
		err := s.telegramService.DeleteClient(msg.Chat.ID)
		if err == nil {
			resp.Text = Tr("msgResetSuccess", s.lang)
		} else {
			resp.Text = Tr("msgInternalError", s.lang)
		}
	} else {
		resp.Text = Tr("cancelled", s.lang)
	}

	s.state = IdleState
	return &resp
}

/*********************************************************
* Helper functions
*********************************************************/

func isTgCommand(msg *tgbotapi.Message) bool {
	if msg.IsCommand() || strings.HasPrefix(msg.Text, "/") {
		return true
	}
	return false
}

func abort(s *TgSession, msg *tgbotapi.Message) *tgbotapi.MessageConfig {
	s.state = IdleState
	s.client = nil
	s.canAcceptPhoto = false
	return IdleState(s, msg)
}

func getLanguagesSelector(resp *tgbotapi.MessageConfig) {
	resp.Text = "Choose your language:"
	row := tgbotapi.NewInlineKeyboardRow()
	langs := GetAvailableLangs()

	for _, lang := range langs {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(display.Self.Name(language.Make(lang)), "lang:"+lang))
	}
	if len(row) > 0 {
		resp.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(row)
	}

}

func parseUuid(text string) string {
	re := regexp.MustCompile("[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
	uuid := re.FindString(text)

	return uuid
}

func (s *TgSession) loadClientLang(chatId int64) {
	if s.lang != "" {
		return
	}
	client, err := s.telegramService.getTgClient(chatId)
	if err == nil && client.Language != "" {
		s.lang = client.Language
	} else {
		s.lang = defaultLang
	}
}

func (s *TgSession) appendChatMenu(resp *tgbotapi.MessageConfig) {
	crmEnabled := s.telegramService.settingService.GetTgCrmEnabled()

	rows := make([][]tgbotapi.InlineKeyboardButton, 0)
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(Tr("menuGetUsage", s.lang), "/"+UsageCmdKey)))
	if crmEnabled {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(Tr("menuOrder", s.lang), "/"+RegisterCmdKey)))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(Tr("menuRefer", s.lang), "/"+ReferToFriendsCmdKey),
		tgbotapi.NewInlineKeyboardButtonData(Tr("menuSupport", s.lang), "/"+ContactSupportCmdKey),
	))
	resp.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		rows...,
	)

}

func (s *TgSession) showAccListKeyboard(resp *tgbotapi.MessageConfig) {
	accList, err := s.telegramService.settingService.GetTgCrmRegAccList()
	if err != nil {
		resp.Text = Tr("msgInternalError", s.lang)
		return
	}

	accList = strings.TrimSpace(accList)
	accounts := strings.Split(accList, "\n")
	row := tgbotapi.NewInlineKeyboardRow()
	for i := 1; i <= len(accounts); i++ {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprint(i), fmt.Sprint(i)))
	}
	if len(row) > 0 {
		resp.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(row)
	}

	resp.Text = Tr("msgChoosePackage", s.lang) + "\n" + accList
}

func (s *TgSession) RenewAccount(chatId int64, uuid string) *tgbotapi.MessageConfig {
	crmEnabled := s.telegramService.settingService.GetTgCrmEnabled()

	s.loadClientLang(chatId)

	resp := tgbotapi.NewMessage(chatId, "")
	if !crmEnabled {
		resp.Text = Tr("msgNotActive", s.lang)
		return &resp
	}

	client, _ := s.telegramService.getTgClient(chatId)
	s.client = client

	if client != nil {
		s.showAccListKeyboard(&resp)
		s.clientRequest = &model.TgClientMsg{
			ChatID: s.client.ChatID,
			Type:   model.Renewal,
			Msg:    "Acc: " + uuid + ",",
		}

		s.state = RegAccTypeState
	} else {
		resp.Text = Tr("msgNotRegisteredEnterLink", s.lang)
		s.state = IdleState
	}
	return &resp
}
