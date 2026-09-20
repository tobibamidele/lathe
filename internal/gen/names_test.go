package gen

import "testing"

func TestPascal(t *testing.T) {
	cases := map[string]string{
		"users": "Users", "user_id": "UserID", "api_key": "APIKey", "password_hash": "PasswordHash",
		"created_at": "CreatedAt", "http_url": "HTTPURL", "id": "ID", "order_items": "OrderItems",
		"2fa_secret": "X2faSecret", "": "X",
	}
	for in, want := range cases {
		if got := pascal(in); got != want {
			t.Errorf("pascal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSingular(t *testing.T) {
	cases := map[string]string{
		"users": "user", "categories": "category", "addresses": "address", "boxes": "box",
		"watches": "watch", "people": "person", "statuses": "status", "news": "news", "series": "series",
		"user_sessions": "user_session", "order_items": "order_item", "class": "class", "status": "status",
		"analysis": "analysis", "data": "data", "cases": "case", "media": "media", "bus": "bus",
		"companies": "company", "posts": "post", "team_people": "team_person",
	}
	for in, want := range cases {
		if got := singular(in); got != want {
			t.Errorf("singular(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParamName(t *testing.T) {
	cases := map[string]string{"ID": "id", "UserID": "userID", "Email": "email", "APIKey": "apiKey", "Type": "typeValue", "Func": "funcValue"}
	for in, want := range cases {
		if got := paramName(in); got != want {
			t.Errorf("paramName(%q) = %q, want %q", in, got, want)
		}
	}
}
