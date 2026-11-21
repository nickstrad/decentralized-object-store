package clean

import "fmt"

func A(x *int) string {
	return fmt.Sprintf("%v %d %%p %5.2s", *x, *x, "ab")
}
