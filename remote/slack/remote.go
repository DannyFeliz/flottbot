package slack

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/nlopes/slack"
	"github.com/target/flottbot/model"
	"github.com/target/flottbot/remote"
)

/*
=======================================
Implementation for the Remote interface
=======================================
*/

// Client struct
type Client struct {
	Token             string
	VerificationToken string
	WorkspaceToken    string
}

// validate that Client adheres to remote interface
var _ remote.Remote = (*Client)(nil)

// instantiate a new slack client
func (c *Client) new() *slack.Client {
	api := slack.New(c.Token)
	return api
}

func (c *Client) Channels() (*model.Channels, error) {
	return nil, nil
}

func (c *Client) Login() (*model.BotUser, error) {
	return nil, nil
}

// Reaction implementation to satisfy remote interface
func (c *Client) Reaction(message model.Message, rule model.Rule, bot *model.Bot) {
	if len(rule.RemoveReaction) > 0 {
		// Init api client
		api := c.new()
		// Grab a reference to the message
		msgRef := slack.NewRefToMessage(message.ChannelID, message.Timestamp)
		// React with desired reaction
		if err := api.RemoveReaction(rule.RemoveReaction, msgRef); err != nil {
			bot.Log.Errorf("Could not add reaction '%s'", err)
			return
		}
		bot.Log.Debugf("Removed reaction '%s' for rule %s", rule.RemoveReaction, rule.Name)
	}
	if len(rule.Reaction) > 0 {
		// Init api client
		api := c.new()
		// Grab a reference to the message
		msgRef := slack.NewRefToMessage(message.ChannelID, message.Timestamp)
		// React with desired reaction
		if err := api.AddReaction(rule.Reaction, msgRef); err != nil {
			bot.Log.Errorf("Could not add reaction '%s'", err)
			return
		}
		bot.Log.Debugf("Added reaction '%s' for rule %s", rule.Reaction, rule.Name)
	}
}

// Read implementation to satisfy remote interface
// Utilizes the Slack API client to read messages from Slack
func (c *Client) Read(inputMsgs chan<- model.Message, rules map[string]model.Rule, bot *model.Bot) {
	// init api client
	api := c.new()

	// get bot channels
	bot.Channels = getChannels(api)

	// get bot id
	rat, err := api.AuthTest()
	if err != nil {
		bot.Log.Errorf("The Slack bot token that was provided was invalid or is unauthorized")
		bot.Log.Warn("Closing Slack message reader")
		return
	}

	// read messages
	if len(c.VerificationToken) > 0 {
		if len(bot.SlackEventsCallbackPath) == 0 {
			bot.Log.Error("Need to specify a callback path for the 'slack_events_callback_path' field in the bot.yml (e.g. \"/slack_events/v1/mybot-v1_events\")")
			bot.Log.Debug("Closing events reader (will not be able to read messages)")
			return
		}
		if !isValidPath(bot.SlackEventsCallbackPath) { // valid path e.g. /slack_events/v1/mybot_dev-v1_events
			bot.Log.Error("Invalid events path. Please double check your path value/syntax (e.g. \"/slack_events/v1/mybot_dev-v1_events\")")
			bot.Log.Debug("Closing events reader (will not be able to read messages)")
			return
		}
		bot.ID = rat.UserID
		readFromEventsAPI(api, c.VerificationToken, inputMsgs, bot)
	} else if len(c.Token) > 0 {
		bot.ID = rat.UserID
		rtm := api.NewRTM()
		readFromRTM(rtm, inputMsgs, bot)
	} else {
		if !bot.CLI {
			bot.Log.Fatal("Did not find either Slack Token or Slack Verification Token. Unable to read from Slack")
		} else {
			bot.Log.Warn("Slack was specified as your chat_application but no Slack Token or Slack Verification Token was provided")
		}
	}
}

// Send implementation to satisfy remote interface
func (c *Client) Send(message model.Message, bot *model.Bot) {
	bot.Log.Debugf("Sending message %s", message.ID)

	api := c.new()

	// check message size and trim if necessary because
	// slack messages have a hard limit of 4000 characters
	if len(message.Output) > 4000 {
		contents := message.Output
		message.Output = contents[:3997] + "..."
	}

	// Timestamp message
	message.EndTime = model.MessageTimestamp()

	// send message  based on type
	switch message.Type {
	case model.MsgTypeDirect, model.MsgTypeChannel, model.MsgTypePrivateChannel:
		send(api, message, bot)
	default:
		bot.Log.Warn("Received unknown  message type - no message to send")
	}
}

var interactionsRouter *mux.Router

// InteractiveComponents implementation to satisfy remote interface
// It will serve as a way for your bot to handle advance messaging, such as message attachments.
// When your bot is up and running, it will have an http/https endpoint to handle rules for sending attachments.
func (c *Client) InteractiveComponents(inputMsgs chan<- model.Message, message *model.Message, rule model.Rule, bot *model.Bot) {
	if bot.InteractiveComponents && len(c.VerificationToken) > 0 {
		if len(bot.SlackInteractionsCallbackPath) == 0 {
			bot.Log.Error("Need to specify a callback path for the 'slack_interactions_callback_path' field in the bot.yml (e.g. \"/slack_events/v1/mybot_dev-v1_interactions\")")
			bot.Log.Warn("Closing interactions reader (will not be able to read interactive components)")
			return
		}
		if interactionsRouter == nil {
			// create router for the Interactive Components server
			interactionsRouter = mux.NewRouter()

			// interaction health check handler
			interactionsRouter.HandleFunc("/interaction_health", getInteractiveComponentHealthHandler(bot)).Methods("GET")

			// Rule handler and endpoint
			ruleHandle := getInteractiveComponentRuleHandler(c.VerificationToken, inputMsgs, message, rule, bot)

			// We use regex for interactions routing for any bot using this framework
			// e.g. /slack_events/v1/mybot_dev-v1_interactions
			if !isValidPath(bot.SlackInteractionsCallbackPath) {
				bot.Log.Error("Invalid events path. Please double check your path value/syntax (e.g. \"/slack_events/v1/mybot_dev-v1_interactions\")")
				bot.Log.Warn("Closing interaction components reader (will not be able to read interactive components)")
				return
			}
			interactionsRouter.HandleFunc(bot.SlackInteractionsCallbackPath, ruleHandle).Methods("POST")

			// start Interactive Components server
			go http.ListenAndServe(":4000", interactionsRouter)
			bot.Log.Infof("Slack Interactive Components server is listening to %s", bot.SlackInteractionsCallbackPath)
		}

		// Process the hit rule for Interactive Components, e.g. interactive messages
		processInteractiveComponentRule(rule, message, bot)
	}
}
